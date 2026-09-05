//go:build integration

package system

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/app/groups"
	"cheapskate/internal/app/port"
	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/app/reconcile"
	"cheapskate/internal/aws/compute"
	computemocks "cheapskate/internal/aws/compute/mocks"
	"cheapskate/internal/aws/tagging"
	taggingmocks "cheapskate/internal/aws/tagging/mocks"
	"cheapskate/internal/core/model"
	"cheapskate/internal/devtools/emutest"
	"cheapskate/internal/state"
)

func TestSlimLifecycle(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	store := state.New(dynamodb.NewFromConfig(cfg), table)
	discoverer := porttest.NewDiscoverer()
	resource := model.Resource{
		Type: model.TypeRdsInstance, Ref: "dev-db", ARN: "arn:aws:rds:ap-northeast-1:123456789012:db:dev-db",
		Tags: map[string]string{model.GroupTagKey: "dev"},
	}
	discoverer.Resources[resource.ARN] = resource
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations[resource.Ref] = model.Observation{State: model.StateRunning}
	notifier := &porttest.Notifier{}
	deps := &reconcile.Deps{
		Store: store, Discoverer: discoverer, Targets: map[model.ResourceType]port.Target{model.TypeRdsInstance: target},
		Notifier: notifier, Location: time.UTC, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	service := groups.New(store, discoverer, nil, time.UTC)
	ctx := context.Background()
	night := time.Date(2026, 9, 3, 21, 0, 0, 0, time.UTC)

	_, err := service.Schedule(ctx, "dev", model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 20 * * *"}, night)
	require.NoError(t, err)
	_, err = reconcile.Run(ctx, nil, deps, night)
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-db"}, target.Stopped)

	target.Observations[resource.Ref] = model.Observation{State: model.StateStopped}
	_, err = service.Override(ctx, "dev", model.OverrideRunning, time.Hour, night)
	require.NoError(t, err)
	_, err = reconcile.Run(ctx, nil, deps, night)
	require.NoError(t, err)
	assert.Equal(t, []string{"dev-db"}, target.Started)

	target.Observations[resource.Ref] = model.Observation{State: model.StateRunning}
	_, err = reconcile.Run(ctx, nil, deps, night.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Len(t, target.Stopped, 2, "expired override must resume the schedule")

	_, err = service.Override(ctx, "dev", model.OverrideDisabled, 0, night.Add(2*time.Hour))
	require.NoError(t, err)
	before := len(target.Stopped) + len(target.Started)
	_, err = reconcile.Run(ctx, nil, deps, night.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, before, len(target.Stopped)+len(target.Started))
	assert.Len(t, notifier.Published, 3)
}

func TestDeleteRaceDoesNotRecreateGroup(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	store := state.New(dynamodb.NewFromConfig(cfg), table)
	ctx := context.Background()
	require.NoError(t, store.CreateGroup(ctx, model.GroupSpec{Name: "dev", Override: model.OverrideRunning}))
	require.NoError(t, store.DeleteGroup(ctx, "dev"))

	err := store.SetSchedule(ctx, "dev", model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 20 * * *"})
	assert.ErrorIs(t, err, state.ErrConflict)
	err = store.ClearOverride(ctx, "dev")
	assert.ErrorIs(t, err, state.ErrConflict)
	group, err := store.GetGroup(ctx, "dev")
	require.NoError(t, err)
	assert.Nil(t, group)
}

func TestConcurrentScheduleAndOverridePreserveBothChanges(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	store := state.New(dynamodb.NewFromConfig(cfg), table)
	ctx := context.Background()
	require.NoError(t, store.CreateGroup(ctx, model.GroupSpec{
		Name: "dev", StartCron: "0 9 * * *", StopCron: "0 20 * * *", Override: model.OverrideRunning,
	}))

	ready := make(chan struct{})
	errs := make(chan error, 2)
	var started sync.WaitGroup
	started.Add(2)
	go func() {
		started.Done()
		<-ready
		errs <- store.SetSchedule(ctx, "dev", model.ScheduleSpec{StartCron: "0 8 * * *", StopCron: "0 19 * * *"})
	}()
	go func() {
		started.Done()
		<-ready
		errs <- store.SetOverride(ctx, "dev", model.OverrideStopped, 0)
	}()
	started.Wait()
	close(ready)
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	group, err := store.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, group)
	assert.Equal(t, "0 8 * * *", group.StartCron)
	assert.Equal(t, "0 19 * * *", group.StopCron)
	assert.Equal(t, model.OverrideStopped, group.Override)
}

func TestTagChangeWithinDiscoveryUsesOnlyTheLastGroup(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	store := state.New(dynamodb.NewFromConfig(cfg), table)
	ctx := context.Background()
	require.NoError(t, store.CreateGroup(ctx, model.GroupSpec{Name: "old", Override: model.OverrideStopped}))
	require.NoError(t, store.CreateGroup(ctx, model.GroupSpec{Name: "new", Override: model.OverrideRunning}))

	client := taggingmocks.NewMockAPI(gomock.NewController(t))
	arn := "arn:aws:rds:ap-northeast-1:123456789012:db:dev-db"
	token := "next"
	gomock.InOrder(
		client.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
			ResourceTagMappingList: []tagtypes.ResourceTagMapping{{
				ResourceARN: aws.String(arn), Tags: []tagtypes.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("old")}},
			}},
			PaginationToken: aws.String(token),
		}, nil),
		client.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
			ResourceTagMappingList: []tagtypes.ResourceTagMapping{{
				ResourceARN: aws.String(arn), Tags: []tagtypes.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("new")}},
			}},
		}, nil),
	)
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["dev-db"] = model.Observation{State: model.StateStopped}
	deps := &reconcile.Deps{
		Store: store, Discoverer: &tagging.Discoverer{Client: client},
		Targets: map[model.ResourceType]port.Target{model.TypeRdsInstance: target}, Location: time.UTC,
	}

	summary, err := reconcile.Run(ctx, nil, deps, time.Now())

	require.NoError(t, err)
	assert.Equal(t, []string{"dev-db"}, target.Started)
	assert.Empty(t, target.Stopped)
	assert.Len(t, summary.Actions, 1)
}

func TestInvalidECSConfigAndDaemonNeverReachModificationAPIs(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	store := state.New(dynamodb.NewFromConfig(cfg), table)
	ctx := context.Background()
	require.NoError(t, store.CreateGroup(ctx, model.GroupSpec{Name: "dev", Override: model.OverrideStopped}))

	controller := gomock.NewController(t)
	ecsClient := computemocks.NewMockEcsAPI(controller)
	ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Times(2).DoAndReturn(
		func(_ context.Context, input *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
			strategy := ecstypes.SchedulingStrategyReplica
			if input.Services[0] == "daemon" {
				strategy = ecstypes.SchedulingStrategyDaemon
			}
			return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
				Status: aws.String("ACTIVE"), SchedulingStrategy: strategy, DesiredCount: 1, RunningCount: 1,
			}}}, nil
		})
	autoScaling := computemocks.NewMockAutoScalingAPI(controller)
	target := &compute.EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"a": {Type: model.TypeEcsService, Ref: "dev/invalid", ARN: "a", Tags: map[string]string{
			model.GroupTagKey: "dev", model.EcsDesiredCountTagKey: "0",
		}},
		"b": {Type: model.TypeEcsService, Ref: "dev/daemon", ARN: "b", Tags: map[string]string{
			model.GroupTagKey: "dev",
		}},
	}
	deps := &reconcile.Deps{
		Store: store, Discoverer: discoverer,
		Targets: map[model.ResourceType]port.Target{model.TypeEcsService: target}, Location: time.UTC,
	}

	summary, err := reconcile.Run(ctx, nil, deps, time.Now())

	require.NoError(t, err)
	assert.Equal(t, 2, summary.Reconciled)
	assert.Len(t, summary.Errors, 2)
	assert.Empty(t, summary.Actions)
}
