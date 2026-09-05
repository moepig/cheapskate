package compute

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/aws/compute/mocks"
	"cheapskate/internal/core/model"
)

func TestEcsDescribeAcceptsReplicaAndRejectsDaemon(t *testing.T) {
	for strategy, wantErr := range map[ecstypes.SchedulingStrategy]bool{
		ecstypes.SchedulingStrategyReplica: false,
		ecstypes.SchedulingStrategyDaemon:  true,
	} {
		t.Run(string(strategy), func(t *testing.T) {
			client := mocks.NewMockEcsAPI(gomock.NewController(t))
			client.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
				Status: aws.String("ACTIVE"), SchedulingStrategy: strategy, DesiredCount: 1, RunningCount: 1,
			}}}, nil)
			observation, err := (&EcsServiceTarget{Ecs: client}).Describe(context.Background(), "dev/api")
			if wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, model.StateRunning, observation.State)
		})
	}
}

func TestEcsStopWithScalableTargetOnlyClampsTarget(t *testing.T) {
	controller := gomock.NewController(t)
	ecsClient := mocks.NewMockEcsAPI(controller)
	autoScaling := mocks.NewMockAutoScalingAPI(controller)
	autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(&aas.DescribeScalableTargetsOutput{
		ScalableTargets: []aastypes.ScalableTarget{{MinCapacity: aws.Int32(1), MaxCapacity: aws.Int32(4)}},
	}, nil)
	autoScaling.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, input *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
			assert.EqualValues(t, 0, aws.ToInt32(input.MinCapacity))
			assert.EqualValues(t, 0, aws.ToInt32(input.MaxCapacity))
			return &aas.RegisterScalableTargetOutput{}, nil
		})
	target := &EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}
	require.NoError(t, target.Stop(context.Background(), model.Resource{Ref: "dev/api"}))
}

func TestEcsStopWithoutScalableTargetOnlyUpdatesDesiredCount(t *testing.T) {
	controller := gomock.NewController(t)
	ecsClient := mocks.NewMockEcsAPI(controller)
	autoScaling := mocks.NewMockAutoScalingAPI(controller)
	autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(&aas.DescribeScalableTargetsOutput{}, nil)
	ecsClient.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, input *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
			assert.EqualValues(t, 0, aws.ToInt32(input.DesiredCount))
			return &ecs.UpdateServiceOutput{}, nil
		})
	target := &EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}
	require.NoError(t, target.Stop(context.Background(), model.Resource{Ref: "dev/api"}))
}

func TestEcsStartRestoresBoundsBeforeDesiredCount(t *testing.T) {
	controller := gomock.NewController(t)
	ecsClient := mocks.NewMockEcsAPI(controller)
	autoScaling := mocks.NewMockAutoScalingAPI(controller)
	resource := model.Resource{Ref: "dev/api", Tags: map[string]string{
		model.EcsDesiredCountTagKey: "2", model.EcsScalingMinTagKey: "1", model.EcsScalingMaxTagKey: "3",
	}}
	gomock.InOrder(
		autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(&aas.DescribeScalableTargetsOutput{ScalableTargets: []aastypes.ScalableTarget{{}}}, nil),
		autoScaling.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, input *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
				assert.EqualValues(t, 1, aws.ToInt32(input.MinCapacity))
				assert.EqualValues(t, 3, aws.ToInt32(input.MaxCapacity))
				return &aas.RegisterScalableTargetOutput{}, nil
			}),
		ecsClient.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, input *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
				assert.EqualValues(t, 2, aws.ToInt32(input.DesiredCount))
				return &ecs.UpdateServiceOutput{}, nil
			}),
	)
	require.NoError(t, (&EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}).Start(context.Background(), resource))
}

func TestEcsRejectsInvalidTagsBeforeModification(t *testing.T) {
	for name, tags := range map[string]map[string]string{
		"zero desired":  {model.EcsDesiredCountTagKey: "0"},
		"empty desired": {model.EcsDesiredCountTagKey: ""},
		"negative min":  {model.EcsScalingMinTagKey: "-1"},
		"inconsistent":  {model.EcsDesiredCountTagKey: "2", model.EcsScalingMinTagKey: "3", model.EcsScalingMaxTagKey: "4"},
	} {
		t.Run(name, func(t *testing.T) {
			controller := gomock.NewController(t)
			target := &EcsServiceTarget{Ecs: mocks.NewMockEcsAPI(controller), AutoScaling: mocks.NewMockAutoScalingAPI(controller)}
			resource := model.Resource{Ref: "dev/api", Tags: tags}
			assert.Error(t, target.Start(context.Background(), resource))
			assert.Error(t, target.Stop(context.Background(), resource))
		})
	}
}

func TestEcsDefaultConfiguration(t *testing.T) {
	config, err := ecsConfigFromTags(nil)
	require.NoError(t, err)
	assert.Equal(t, ecsConfig{desired: 1, minimum: 1, maximum: 1}, config)
}
