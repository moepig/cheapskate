//go:build image

// ビルド済みの reconciler イメージへ、定期呼び出しと任意の JSON ペイロードを投入する
//
// 検証の対象はイメージの振る舞いであり、RIE と testcontainers は実行の手段である
// パッケージの位置づけとハーネスの前提は、doc.go と harness_test.go を参照
//
//	make image-test   # = go test -tags image -count=1 ./tests/image/
package image

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	resourcegroupstaggingapiTypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/app/groups"
	"cheapskate/internal/app/reconcile"
	"cheapskate/internal/aws/tagging"
	"cheapskate/internal/core/model"
	"cheapskate/internal/devtools/emutest"
	"cheapskate/internal/state"
)

func TestReconcilerImageHandlesEventPayloads(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	_, err := dynamodb.NewFromConfig(cfg).PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item: map[string]types.AttributeValue{
			"pk":      &types.AttributeValueMemberS{Value: "CONFIG"},
			"sk":      &types.AttributeValueMemberS{Value: "GROUP#broken"},
			"unknown": &types.AttributeValueMemberS{Value: "value"},
		},
	})
	require.NoError(t, err)

	reconciler := startUnderRIE(t, buildImage(t, "reconciler"), emulatorEnv(t, table), []byte("{}"))

	t.Run("periodic payload", func(t *testing.T) {
		summary := requireSummaryResponse(t, reconciler.invoke(t, []byte("{}")))
		assert.Equal(t, 0, summary.Reconciled)
		assert.Empty(t, summary.Actions)
		require.Len(t, summary.Errors, 1)
		assert.Equal(t, "broken", summary.Errors[0].Group)
	})

	t.Run("arbitrary payload", func(t *testing.T) {
		summary := requireSummaryResponse(t, reconciler.invoke(t, []byte("[]")))
		assert.Equal(t, 0, summary.Reconciled)
		require.Len(t, summary.Errors, 1)
	})
}

func TestReconcilerImageChangesECSServiceAcrossInvocations(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	reconciler := startUnderRIE(t, buildImage(t, "reconciler"), emulatorEnv(t, table), []byte("{}"))

	ctx := context.Background()
	ecsClient := ecs.NewFromConfig(cfg)
	groupName := emutest.RandomName("image-group")
	taggingClient := resourcegroupstaggingapi.NewFromConfig(cfg)
	removeStaleImageTestTags(t, ctx, taggingClient)
	cluster, service := createImageTestECSService(t, ctx, ecsClient, taggingClient, groupName)
	waitForImageTestResource(t, ctx, taggingClient, groupName)
	store := state.New(dynamodb.NewFromConfig(cfg), table)
	groupService := groups.New(store, nil, nil, time.UTC)
	_, err := groupService.Override(ctx, groupName, model.OverrideStopped, 0, time.Now().UTC())
	require.NoError(t, err)

	stopped := requireSummaryResponse(t, reconciler.invoke(t, []byte("{}")))
	require.Len(t, stopped.Actions, 1, "summary=%+v", stopped)
	assert.Equal(t, model.ActionStop, stopped.Actions[0].Action)
	waitForImageTestECSService(t, ctx, ecsClient, cluster, service, func(observation ecstypes.Service) bool {
		return observation.DesiredCount == 0 && observation.RunningCount == 0 && observation.PendingCount == 0
	})

	_, err = groupService.Override(ctx, groupName, model.OverrideRunning, 0, time.Now().UTC())
	require.NoError(t, err)
	running := requireSummaryResponse(t, reconciler.invoke(t, []byte("[]")))
	require.Len(t, running.Actions, 1)
	assert.Equal(t, model.ActionStart, running.Actions[0].Action)
	waitForImageTestECSService(t, ctx, ecsClient, cluster, service, func(observation ecstypes.Service) bool {
		return observation.DesiredCount == 1
	})

	converged := requireSummaryResponse(t, reconciler.invoke(t, []byte("{}")))
	assert.Empty(t, converged.Actions)
	assert.Empty(t, converged.Errors)
}

func createImageTestECSService(t *testing.T, ctx context.Context, client *ecs.Client, taggingClient *resourcegroupstaggingapi.Client, groupName string) (string, string) {
	t.Helper()
	clusterName := emutest.RandomName("image-cluster")
	serviceName := emutest.RandomName("image-service")
	_, err := client.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(clusterName)})
	require.NoError(t, err)
	taskDefinition, err := client.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(emutest.RandomName("image-task")),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name: aws.String("app"), Image: aws.String("nginx:latest@sha256:b34848eff6db786b6b1282d3a9c3fd0b5563dfb6d261df4923378b419e0d24f0"), Essential: aws.Bool(true),
		}},
	})
	require.NoError(t, err)
	created, err := client.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster: aws.String(clusterName), ServiceName: aws.String(serviceName),
		TaskDefinition: aws.String(aws.ToString(taskDefinition.TaskDefinition.TaskDefinitionArn)), DesiredCount: aws.Int32(1),
		LaunchType: ecstypes.LaunchTypeFargate, SchedulingStrategy: ecstypes.SchedulingStrategyReplica,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
			Subnets: []string{"subnet-00000000"}, AssignPublicIp: ecstypes.AssignPublicIpEnabled,
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, created.Service)
	require.NotNil(t, created.Service.ServiceArn)
	_, err = taggingClient.TagResources(ctx, &resourcegroupstaggingapi.TagResourcesInput{
		ResourceARNList: []string{aws.ToString(created.Service.ServiceArn)},
		Tags:            map[string]string{model.GroupTagKey: groupName, model.EcsDesiredCountTagKey: "1"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = taggingClient.UntagResources(context.Background(), &resourcegroupstaggingapi.UntagResourcesInput{
			ResourceARNList: []string{aws.ToString(created.Service.ServiceArn)}, TagKeys: []string{model.GroupTagKey, model.EcsDesiredCountTagKey},
		})
		_, _ = client.DeleteService(context.Background(), &ecs.DeleteServiceInput{Cluster: aws.String(clusterName), Service: aws.String(serviceName), Force: aws.Bool(true)})
		_, _ = client.DeleteCluster(context.Background(), &ecs.DeleteClusterInput{Cluster: aws.String(clusterName)})
	})
	return clusterName, serviceName
}

func waitForImageTestECSService(t *testing.T, ctx context.Context, client *ecs.Client, cluster, service string, predicate func(ecstypes.Service) bool) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		out, err := client.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(cluster), Services: []string{service}})
		if err == nil && len(out.Services) == 1 && predicate(out.Services[0]) {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("ECS service %s/%s did not reach the expected state", cluster, service)
}

func waitForImageTestResource(t *testing.T, ctx context.Context, client *resourcegroupstaggingapi.Client, groupName string) {
	t.Helper()
	discoverer := &tagging.Discoverer{Client: client}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resources, err := discoverer.Discover(ctx)
		if err == nil {
			for _, resource := range resources {
				if resource.Tags[model.GroupTagKey] == groupName {
					return
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("ECS resource with group tag %q was not discoverable", groupName)
}

func removeStaleImageTestTags(t *testing.T, ctx context.Context, client *resourcegroupstaggingapi.Client) {
	t.Helper()
	out, err := client.GetResources(ctx, &resourcegroupstaggingapi.GetResourcesInput{
		TagFilters:          []resourcegroupstaggingapiTypes.TagFilter{{Key: aws.String(model.GroupTagKey)}},
		ResourceTypeFilters: []string{"ecs:service"}, ResourcesPerPage: aws.Int32(100),
	})
	require.NoError(t, err)
	for _, mapping := range out.ResourceTagMappingList {
		for _, resourceTag := range mapping.Tags {
			if aws.ToString(resourceTag.Key) == model.GroupTagKey && strings.HasPrefix(aws.ToString(resourceTag.Value), "image-group-") {
				_, err := client.UntagResources(ctx, &resourcegroupstaggingapi.UntagResourcesInput{
					ResourceARNList: []string{aws.ToString(mapping.ResourceARN)}, TagKeys: []string{model.GroupTagKey, model.EcsDesiredCountTagKey},
				})
				require.NoError(t, err)
				break
			}
		}
	}
}

func requireSummaryResponse(t *testing.T, body []byte) reconcile.Summary {
	t.Helper()
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))
	assert.NotContains(t, raw, "errorMessage")
	assert.NotContains(t, raw, "errorType")
	require.Contains(t, raw, "reconciled")
	require.Contains(t, raw, "actions")
	require.Contains(t, raw, "errors")

	var summary reconcile.Summary
	require.NoError(t, json.Unmarshal(body, &summary))
	return summary
}

func TestReconcilerImageRejectsInvalidTimezone(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	assertRejectsInvalidTimezone(t, buildImage(t, "reconciler"), emulatorEnv(t, table))
}
