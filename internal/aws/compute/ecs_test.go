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
			controller := gomock.NewController(t)
			client := mocks.NewMockEcsAPI(controller)
			autoScaling := mocks.NewMockAutoScalingAPI(controller)
			client.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
				Status: aws.String("ACTIVE"), SchedulingStrategy: strategy, DesiredCount: 1, RunningCount: 1,
			}}}, nil)
			autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).AnyTimes().Return(&aas.DescribeScalableTargetsOutput{}, nil)
			observation, err := (&EcsServiceTarget{Ecs: client, AutoScaling: autoScaling}).Describe(context.Background(), model.Resource{Ref: "dev/api"})
			if wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, model.StateRunning, observation.State)
		})
	}
}

func TestEcsDescribeMarksRunningServiceForStartWhenScalableBoundsDiffer(t *testing.T) {
	controller := gomock.NewController(t)
	ecsClient := mocks.NewMockEcsAPI(controller)
	autoScaling := mocks.NewMockAutoScalingAPI(controller)
	ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
		Status: aws.String("ACTIVE"), SchedulingStrategy: ecstypes.SchedulingStrategyReplica, DesiredCount: 2, RunningCount: 2,
	}}}, nil)
	autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(&aas.DescribeScalableTargetsOutput{
		ScalableTargets: []aastypes.ScalableTarget{{MinCapacity: aws.Int32(0), MaxCapacity: aws.Int32(0)}},
	}, nil)

	observation, err := (&EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}).Describe(context.Background(), model.Resource{
		Ref:  "dev/api",
		Tags: map[string]string{model.EcsDesiredCountTagKey: "2", model.EcsScalingMinTagKey: "1", model.EcsScalingMaxTagKey: "3"},
	})

	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, observation.State)
	assert.True(t, observation.NeedsStart)
}

func TestEcsServiceStateAllowsStopBeforeTaskCountConverges(t *testing.T) {
	assert.Equal(t, model.StateRunning, ecsServiceState(2, 1, 0))
	assert.Equal(t, model.StateRunning, ecsServiceState(2, 0, 1))
	assert.Equal(t, model.ActionStop, model.DecideAction(model.DesiredStopped, ecsServiceState(2, 1, 0)))
	assert.Equal(t, model.StateStopped, ecsServiceState(0, 0, 0))
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
		ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
			Status: aws.String("ACTIVE"), SchedulingStrategy: ecstypes.SchedulingStrategyReplica, DesiredCount: 0,
		}}}, nil),
		ecsClient.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, input *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
				assert.EqualValues(t, 2, aws.ToInt32(input.DesiredCount))
				return &ecs.UpdateServiceOutput{}, nil
			}),
	)
	require.NoError(t, (&EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}).Start(context.Background(), resource))
}

func TestEcsStartRepairsBoundsWithoutUpdatingDesiredCount(t *testing.T) {
	controller := gomock.NewController(t)
	ecsClient := mocks.NewMockEcsAPI(controller)
	autoScaling := mocks.NewMockAutoScalingAPI(controller)
	resource := model.Resource{Ref: "dev/api", Tags: map[string]string{
		model.EcsDesiredCountTagKey: "2", model.EcsScalingMinTagKey: "1", model.EcsScalingMaxTagKey: "3",
	}}
	gomock.InOrder(
		autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(&aas.DescribeScalableTargetsOutput{
			ScalableTargets: []aastypes.ScalableTarget{{MinCapacity: aws.Int32(0), MaxCapacity: aws.Int32(0)}},
		}, nil),
		autoScaling.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, input *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
				assert.EqualValues(t, 1, aws.ToInt32(input.MinCapacity))
				assert.EqualValues(t, 3, aws.ToInt32(input.MaxCapacity))
				return &aas.RegisterScalableTargetOutput{}, nil
			}),
		ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
			Status: aws.String("ACTIVE"), SchedulingStrategy: ecstypes.SchedulingStrategyReplica, DesiredCount: 2, RunningCount: 2,
		}}}, nil),
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
