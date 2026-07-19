package target

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

	"cheapskate/internal/mocks"
	"cheapskate/internal/model"
)

func i32(v int32) *int32 { return &v }

// ecsDescribe wires an EXPECT for a single DescribeServices call returning the given desiredCount.
func ecsDescribe(e *mocks.MockEcsAPI, desiredCount int32) {
	e.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
		Status:       aws.String("ACTIVE"),
		DesiredCount: desiredCount,
	}}}, nil)
}

// aasDescribe wires an EXPECT for a single DescribeScalableTargets call returning target (nil for none).
func aasDescribe(a *mocks.MockAutoScalingAPI, target *aastypes.ScalableTarget) {
	out := &aas.DescribeScalableTargetsOutput{}
	if target != nil {
		out.ScalableTargets = []aastypes.ScalableTarget{*target}
	}
	a.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(out, nil)
}

func TestEcsPrepareStopReturnsCountAndScaling(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	ecsDescribe(e, 3)
	aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: i32(2), MaxCapacity: i32(6)})
	// No RegisterScalableTarget/UpdateService EXPECT: PrepareStop must not mutate anything.
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	saved, err := tgt.PrepareStop(context.Background(), "dev/api", model.Config{}, model.Status{})
	require.NoError(t, err)
	require.NotNil(t, saved.DesiredCount)
	assert.Equal(t, int32(3), *saved.DesiredCount)
	require.NotNil(t, saved.ScalingMin)
	require.NotNil(t, saved.ScalingMax)
	assert.Equal(t, int32(2), *saved.ScalingMin)
	assert.Equal(t, int32(6), *saved.ScalingMax)
}

func TestEcsPrepareStopKeepsExistingSavedValuesWhenAlreadyZero(t *testing.T) {
	// desiredCount and scaling already at 0/0 means cheapskate (or someone) already stopped this; PrepareStop must not report a zero that would clobber the real saved value (B-2).
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	ecsDescribe(e, 0)
	aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: i32(0), MaxCapacity: i32(0)})
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	saved, err := tgt.PrepareStop(context.Background(), "dev/api", model.Config{}, model.Status{})
	require.NoError(t, err)
	assert.Nil(t, saved.DesiredCount, "desired count must be left nil")
	assert.Nil(t, saved.ScalingMin, "scaling min must be left nil")
	assert.Nil(t, saved.ScalingMax, "scaling max must be left nil")
}

func TestEcsStopPinsScalingAndZeroesDesiredCount(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: i32(2), MaxCapacity: i32(6)})
	gomock.InOrder(
		a.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, in *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
				assert.Equal(t, int32(0), *in.MinCapacity)
				assert.Equal(t, int32(0), *in.MaxCapacity)
				return &aas.RegisterScalableTargetOutput{}, nil
			}),
		e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
				assert.Equal(t, int32(0), *in.DesiredCount)
				return &ecs.UpdateServiceOutput{}, nil
			}),
	)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	require.NoError(t, tgt.Stop(context.Background(), "dev/api", model.Config{}, model.Status{}))
}

func TestEcsStopWithoutScalingTarget(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, nil)
	// No RegisterScalableTarget EXPECT: no scaling target means it must not register.
	e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).Return(&ecs.UpdateServiceOutput{}, nil)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	require.NoError(t, tgt.Stop(context.Background(), "dev/api", model.Config{}, model.Status{}))
}

func TestEcsStartRestoresScalingAndCount(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	a.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
			assert.Equal(t, int32(2), *in.MinCapacity)
			assert.Equal(t, int32(6), *in.MaxCapacity)
			return &aas.RegisterScalableTargetOutput{}, nil
		})
	e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
			assert.Equal(t, int32(3), *in.DesiredCount)
			return &ecs.UpdateServiceOutput{}, nil
		})
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}
	status := model.Status{SavedDesiredCount: i32(3), SavedScalingMin: i32(2), SavedScalingMax: i32(6)}

	_, err := tgt.Start(context.Background(), "dev/api", model.Config{}, status)
	require.NoError(t, err)
}

func TestEcsRestoreCountPriority(t *testing.T) {
	cases := []struct {
		cfg    model.Config
		status model.Status
		want   int32
	}{
		{model.Config{RestoreCount: i32(5)}, model.Status{SavedDesiredCount: i32(3)}, 5},
		{model.Config{}, model.Status{SavedDesiredCount: i32(3)}, 3},
		{model.Config{}, model.Status{}, 1},
		{model.Config{}, model.Status{SavedDesiredCount: i32(0)}, 1}, // stopped while already 0
	}
	for i, tc := range cases {
		assert.Equal(t, tc.want, restoreCount(tc.cfg, tc.status), "case %d", i)
	}
}

func TestEcsDescribeStates(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	ecsDescribe(e, 2)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}
	obs, err := tgt.Describe(context.Background(), "dev/api")
	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, obs.State)
	require.NotNil(t, obs.DesiredCount)
	assert.Equal(t, int32(2), *obs.DesiredCount)

	e2 := mocks.NewMockEcsAPI(ctrl)
	ecsDescribe(e2, 0)
	tgt = &EcsServiceTarget{Ecs: e2, AutoScaling: a}
	obs, err = tgt.Describe(context.Background(), "dev/api")
	require.NoError(t, err)
	assert.Equal(t, model.StateStopped, obs.State)

	_, err = tgt.Describe(context.Background(), "noslash")
	require.Error(t, err, "want error for malformed ecs ref")
}
