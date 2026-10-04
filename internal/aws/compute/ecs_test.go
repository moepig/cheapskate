package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/app/port"
	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/app/reconcile"
	"cheapskate/internal/aws/compute/mocks"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	statemocks "cheapskate/internal/state/mocks"
)

func TestEcsDescribeAcceptsReplicaAndRejectsDaemon(t *testing.T) {
	for strategy, wantErr := range map[ecstypes.SchedulingStrategy]bool{
		ecstypes.SchedulingStrategyReplica: false,
		"":                                 false,
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

func TestEcsDescribeMarksStoppedServiceForStopWhenScalableBoundsDiffer(t *testing.T) {
	controller := gomock.NewController(t)
	ecsClient := mocks.NewMockEcsAPI(controller)
	autoScaling := mocks.NewMockAutoScalingAPI(controller)
	ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
		Status: aws.String("ACTIVE"), SchedulingStrategy: ecstypes.SchedulingStrategyReplica,
	}}}, nil)
	autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(&aas.DescribeScalableTargetsOutput{
		ScalableTargets: []aastypes.ScalableTarget{{MinCapacity: aws.Int32(0), MaxCapacity: aws.Int32(3)}},
	}, nil)

	observation, err := (&EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}).Describe(context.Background(), model.Resource{Ref: "dev/api"})

	require.NoError(t, err)
	assert.Equal(t, model.StateStopped, observation.State)
	assert.True(t, observation.NeedsStop)
}

func TestEcsServiceStateAllowsStopBeforeTaskCountConverges(t *testing.T) {
	assert.Equal(t, model.StateRunning, ecsServiceState(2, 1, 0))
	assert.Equal(t, model.StateRunning, ecsServiceState(2, 0, 1))
	assert.Equal(t, model.ActionStop, model.DecideAction(model.DesiredStopped, ecsServiceState(2, 1, 0)))
	assert.Equal(t, model.StateStopped, ecsServiceState(0, 0, 0))
}

// scalable target の停止は、上下限 0/0 と Scheduled Scaling の一時停止を 1 回の更新で適用する。
func TestEcsStopWithScalableTargetClampsAndSuspendsTarget(t *testing.T) {
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
			require.NotNil(t, input.SuspendedState)
			assert.Equal(t, aws.Bool(true), input.SuspendedState.ScheduledScalingSuspended)
			assert.Nil(t, input.SuspendedState.DynamicScalingInSuspended)
			assert.Nil(t, input.SuspendedState.DynamicScalingOutSuspended)
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

// 起動台数の設定が成功するまで上下限を固定し、成功後に通常のスケーリング範囲へ戻す。
func TestEcsStartPinsBoundsUntilDesiredCountIsSet(t *testing.T) {
	for _, desired := range []int32{0, 2} {
		t.Run(fmt.Sprint(desired), func(t *testing.T) {
			controller := gomock.NewController(t)
			ecsClient := mocks.NewMockEcsAPI(controller)
			autoScaling := mocks.NewMockAutoScalingAPI(controller)
			calls := []any{
				ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{Status: aws.String("ACTIVE")}}}, nil),
				autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(&aas.DescribeScalableTargetsOutput{
					ScalableTargets: []aastypes.ScalableTarget{{MinCapacity: aws.Int32(0), MaxCapacity: aws.Int32(0)}},
				}, nil),
				autoScaling.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(assertScalableBounds(t, 2, 2, true)),
				ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
					Status: aws.String("ACTIVE"), SchedulingStrategy: ecstypes.SchedulingStrategyReplica, DesiredCount: desired,
				}}}, nil),
			}
			if desired != 2 {
				calls = append(calls, ecsClient.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, input *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
						assert.EqualValues(t, 2, aws.ToInt32(input.DesiredCount))
						return &ecs.UpdateServiceOutput{}, nil
					}))
			}
			calls = append(calls, autoScaling.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(assertScalableBounds(t, 1, 3, false)))
			gomock.InOrder(calls...)
			target := &EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}
			require.NoError(t, target.Start(context.Background(), ecsReconcileResource()))
		})
	}
}

// 固定台数の scalable target も起動設定の完了後に Scheduled Scaling を再開し、target がない場合は台数だけを設定する。
func TestEcsStartWithFixedBoundsOrWithoutScalableTarget(t *testing.T) {
	for _, scalable := range []bool{false, true} {
		t.Run(fmt.Sprint(scalable), func(t *testing.T) {
			controller := gomock.NewController(t)
			ecsClient := mocks.NewMockEcsAPI(controller)
			autoScaling := mocks.NewMockAutoScalingAPI(controller)
			resource := model.Resource{Ref: "dev/api", EcsMaxCount: 2, Tags: map[string]string{model.EcsDesiredCountTagKey: "2"}}
			out := &aas.DescribeScalableTargetsOutput{}
			if scalable {
				out.ScalableTargets = []aastypes.ScalableTarget{{MinCapacity: aws.Int32(0), MaxCapacity: aws.Int32(0)}}
			}
			calls := []any{
				ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{Status: aws.String("ACTIVE")}}}, nil),
				autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(out, nil),
			}
			var desired int32
			if scalable {
				calls = append(calls, autoScaling.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(assertScalableBounds(t, 2, 2, true)))
				desired = 2
				calls = append(calls, ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{Status: aws.String("ACTIVE"), DesiredCount: desired}}}, nil))
				calls = append(calls, autoScaling.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(assertScalableBounds(t, 2, 2, false)))
			}
			if !scalable {
				calls = append(calls, ecsClient.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, input *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
						assert.EqualValues(t, 2, aws.ToInt32(input.DesiredCount))
						return &ecs.UpdateServiceOutput{}, nil
					}))
			}
			gomock.InOrder(calls...)
			require.NoError(t, (&EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}).Start(context.Background(), resource))
		})
	}
}

// 各 API 境界で一度だけ失敗させ、新しい target による次回 reconcile で復旧することを確認する。
// 応答だけが失われた場合は既存の成功結果を維持し、収束後の操作と通知を抑止する。
func TestEcsPartialStartFailuresRecoverOnNextReconcile(t *testing.T) {
	var cases []struct {
		failure string
		paused  bool
	}
	for _, paused := range []bool{false, true} {
		for _, failure := range []string{"pin", "pin response lost", "describe", "update", "release", "release response lost"} {
			cases = append(cases, struct {
				failure string
				paused  bool
			}{failure: failure, paused: paused})
		}
	}
	for _, test := range cases {
		failure := test.failure
		t.Run(fmt.Sprintf("%s/paused=%t", failure, test.paused), func(t *testing.T) {
			controller := gomock.NewController(t)
			ecsClient := mocks.NewMockEcsAPI(controller)
			autoScaling := mocks.NewMockAutoScalingAPI(controller)
			var desired, minimum, maximum int32
			paused := false
			failed := false
			modifications := 0
			apiErr := errors.New("temporary " + failure + " failure")
			autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
				func(context.Context, *aas.DescribeScalableTargetsInput, ...func(*aas.Options)) (*aas.DescribeScalableTargetsOutput, error) {
					return &aas.DescribeScalableTargetsOutput{ScalableTargets: []aastypes.ScalableTarget{{
						MinCapacity: aws.Int32(minimum), MaxCapacity: aws.Int32(maximum), SuspendedState: &aastypes.SuspendedState{ScheduledScalingSuspended: aws.Bool(paused)},
					}}}, nil
				})
			autoScaling.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
				func(_ context.Context, input *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
					modifications++
					nextMin, nextMax := aws.ToInt32(input.MinCapacity), aws.ToInt32(input.MaxCapacity)
					phase := "release"
					if nextMin == 2 && nextMax == 2 {
						phase = "pin"
						assert.Equal(t, aws.Bool(true), input.SuspendedState.ScheduledScalingSuspended)
					} else {
						assert.EqualValues(t, 1, nextMin)
						assert.EqualValues(t, 3, nextMax)
						assert.EqualValues(t, 2, desired)
						assert.Equal(t, aws.Bool(test.paused), input.SuspendedState.ScheduledScalingSuspended)
					}
					if !failed && failure == phase {
						failed = true
						return nil, apiErr
					}
					minimum, maximum = nextMin, nextMax
					paused = aws.ToBool(input.SuspendedState.ScheduledScalingSuspended)
					desired = min(max(desired, minimum), maximum)
					if !failed && failure == phase+" response lost" {
						failed = true
						return nil, apiErr
					}
					return &aas.RegisterScalableTargetOutput{}, nil
				})
			ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
				func(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
					if !failed && failure == "describe" && minimum == 2 {
						failed = true
						return nil, apiErr
					}
					observed := desired
					if !failed && failure == "update" && minimum == 2 {
						observed = 0
					}
					return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
						Status: aws.String("ACTIVE"), SchedulingStrategy: ecstypes.SchedulingStrategyReplica,
						DesiredCount: observed, RunningCount: observed,
					}}}, nil
				})
			ecsClient.EXPECT().UpdateService(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
				func(_ context.Context, input *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
					modifications++
					assert.EqualValues(t, 2, aws.ToInt32(input.DesiredCount))
					if !failed && failure == "update" {
						failed = true
						return nil, apiErr
					}
					desired = aws.ToInt32(input.DesiredCount)
					return &ecs.UpdateServiceOutput{}, nil
				})
			notifier := &porttest.Notifier{}
			cycle := func() reconcile.Summary {
				target := &EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}
				resource := ecsReconcileResource()
				resource.Tags[model.EcsScheduledScalingPausedTagKey] = fmt.Sprint(test.paused)
				result, err := reconcile.Run(context.Background(), nil, ecsReconcileDeps(resource, target, notifier), time.Now())
				require.NoError(t, err)
				return result
			}
			first := cycle()
			require.True(t, failed)
			require.Len(t, first.Errors, 1)
			assert.Contains(t, first.Errors[0].Error, apiErr.Error())
			assert.Empty(t, first.Actions)
			assert.Empty(t, notifier.Published)

			second := cycle()
			assert.Empty(t, second.Errors)
			wantActions := 1
			if failure == "release response lost" {
				wantActions = 0
			}
			assert.Len(t, second.Actions, wantActions)
			assert.EqualValues(t, 2, desired)
			assert.EqualValues(t, 1, minimum)
			assert.EqualValues(t, 3, maximum)
			assert.Equal(t, test.paused, paused)
			before := modifications
			third := cycle()
			assert.Empty(t, third.Actions)
			assert.Empty(t, third.Errors)
			assert.Equal(t, before, modifications)
			assert.Len(t, notifier.Published, wantActions)
		})
	}
}

// 起動台数のタグと異なる稼働台数を返し、通常の台数変更に対する操作と通知がないことを確認する。
func TestEcsReconcilePreservesRunningDesiredCount(t *testing.T) {
	for _, scalable := range []bool{false, true} {
		for _, desired := range []int32{1, 3} {
			t.Run(fmt.Sprintf("scalable=%t/desired=%d", scalable, desired), func(t *testing.T) {
				controller := gomock.NewController(t)
				ecsClient := mocks.NewMockEcsAPI(controller)
				autoScaling := mocks.NewMockAutoScalingAPI(controller)
				ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
					Status: aws.String("ACTIVE"), SchedulingStrategy: ecstypes.SchedulingStrategyReplica,
					DesiredCount: desired, RunningCount: desired,
				}}}, nil)
				out := &aas.DescribeScalableTargetsOutput{}
				if scalable {
					out.ScalableTargets = []aastypes.ScalableTarget{{MinCapacity: aws.Int32(1), MaxCapacity: aws.Int32(3)}}
				}
				autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(out, nil)
				notifier := &porttest.Notifier{}
				target := &EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}
				summary, err := reconcile.Run(context.Background(), nil, ecsReconcileDeps(ecsReconcileResource(), target, notifier), time.Now())
				require.NoError(t, err)
				assert.Equal(t, 1, summary.Reconciled)
				assert.Empty(t, summary.Actions)
				assert.Empty(t, summary.Errors)
				assert.Empty(t, notifier.Published)
			})
		}
	}
}

func ecsReconcileResource() model.Resource {
	return model.Resource{Type: model.TypeEcsService, Ref: "dev/api", ARN: "arn:aws:ecs:ap-northeast-1:123456789012:service/dev/api", EcsMaxCount: 3, Tags: map[string]string{
		model.GroupTagKey: "dev", model.EcsDesiredCountTagKey: "2", model.EcsScalingMinTagKey: "1", model.EcsScalingMaxTagKey: "3",
	}}
}

type ecsReconcileStore struct{}

func (ecsReconcileStore) ListGroups(context.Context) ([]state.GroupRow, error) {
	return []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideRunning, EcsMaxCount: 3}}}, nil
}

func ecsReconcileDeps(resource model.Resource, target *EcsServiceTarget, notifier *porttest.Notifier) *reconcile.Deps {
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{resource.ARN: resource}
	return &reconcile.Deps{
		Store: ecsReconcileStore{}, Discoverer: discoverer,
		Targets: map[model.ResourceType]port.Target{model.TypeEcsService: target}, Notifier: notifier,
		Location: time.UTC, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func assertScalableBounds(t *testing.T, minimum, maximum int32, paused bool) func(context.Context, *aas.RegisterScalableTargetInput, ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
	t.Helper()
	return func(_ context.Context, input *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
		assert.Equal(t, minimum, aws.ToInt32(input.MinCapacity))
		assert.Equal(t, maximum, aws.ToInt32(input.MaxCapacity))
		require.NotNil(t, input.SuspendedState)
		assert.Equal(t, aws.Bool(paused), input.SuspendedState.ScheduledScalingSuspended)
		assert.Nil(t, input.SuspendedState.DynamicScalingInSuspended)
		assert.Nil(t, input.SuspendedState.DynamicScalingOutSuspended)
		return &aas.RegisterScalableTargetOutput{}, nil
	}
}

func TestEcsRejectsInvalidTagsBeforeModification(t *testing.T) {
	for name, tags := range map[string]map[string]string{
		"zero desired":  {model.EcsDesiredCountTagKey: "0"},
		"empty desired": {model.EcsDesiredCountTagKey: ""},
		"negative min":  {model.EcsScalingMinTagKey: "-1"},
		"inconsistent":  {model.EcsDesiredCountTagKey: "2", model.EcsScalingMinTagKey: "3", model.EcsScalingMaxTagKey: "4"},
		"empty pause":   {model.EcsScheduledScalingPausedTagKey: ""},
		"invalid pause": {model.EcsScheduledScalingPausedTagKey: "TRUE"},
	} {
		t.Run(name, func(t *testing.T) {
			controller := gomock.NewController(t)
			target := &EcsServiceTarget{Ecs: mocks.NewMockEcsAPI(controller), AutoScaling: mocks.NewMockAutoScalingAPI(controller)}
			resource := model.Resource{Ref: "dev/api", EcsMaxCount: 3, Tags: tags}
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

func TestEcsConfigurationAcceptsInt32LimitAndRejectsOverflow(t *testing.T) {
	config, err := ecsConfigFromTags(map[string]string{
		model.EcsDesiredCountTagKey: "2147483647", model.EcsScalingMinTagKey: "2147483647", model.EcsScalingMaxTagKey: "2147483647",
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2147483647, config.desired)

	_, err = ecsConfigFromTags(map[string]string{model.EcsDesiredCountTagKey: "2147483648"})
	assert.ErrorContains(t, err, "not an integer")
}

// 登録レコードから上限を読み、上限内の起動だけが更新 API と通知へ到達することを確認する。
// タグや探索結果に含まれる上限値は、登録レコードの上限を変更できない。
func TestEcsReconcileEnforcesRegisteredCountLimit(t *testing.T) {
	for name, test := range map[string]struct {
		limit   int32
		tags    map[string]string
		desired int32
		wantErr string
	}{
		"default count":             {limit: 1, desired: 1},
		"at limit":                  {limit: 3, tags: map[string]string{model.EcsDesiredCountTagKey: "3"}, desired: 3},
		"missing limit":             {wantErr: "ecs_max_count must be registered"},
		"desired exceeds limit":     {limit: 3, tags: map[string]string{model.EcsDesiredCountTagKey: "1000"}, wantErr: "cheapskate/desired-count=1000 exceeds group ecs_max_count=3"},
		"scaling max exceeds limit": {limit: 3, tags: map[string]string{model.EcsDesiredCountTagKey: "2", model.EcsScalingMaxTagKey: "1000"}, wantErr: "cheapskate/scaling-max=1000 exceeds group ecs_max_count=3"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			controller := gomock.NewController(t)
			api, _ := statemocks.NewDynaStore(controller)
			store := state.New(api, "table")
			require.NoError(t, store.CreateGroup(ctx, model.GroupSpec{Name: "dev", Override: model.OverrideRunning, EcsMaxCount: test.limit}))
			resource := ecsReconcileResource()
			resource.Tags = map[string]string{model.GroupTagKey: "dev", "cheapskate/ecs_max_count": "1000"}
			for key, value := range test.tags {
				resource.Tags[key] = value
			}
			resource.EcsMaxCount = 1000
			ecsClient := mocks.NewMockEcsAPI(controller)
			autoScaling := mocks.NewMockAutoScalingAPI(controller)
			reads := 1
			if test.wantErr == "" {
				reads = 2
				ecsClient.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, input *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
						assert.Equal(t, test.desired, aws.ToInt32(input.DesiredCount))
						return &ecs.UpdateServiceOutput{}, nil
					})
			}
			ecsClient.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Times(reads).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{Status: aws.String("ACTIVE")}}}, nil)
			autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Times(reads).Return(&aas.DescribeScalableTargetsOutput{}, nil)
			notifier := &porttest.Notifier{}
			deps := ecsReconcileDeps(resource, &EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}, notifier)
			deps.Store = store
			summary, err := reconcile.Run(ctx, nil, deps, time.Now())
			require.NoError(t, err)
			if test.wantErr != "" {
				require.Len(t, summary.Errors, 1)
				assert.Contains(t, summary.Errors[0].Error, test.wantErr)
				assert.Empty(t, summary.Actions)
				assert.Empty(t, notifier.Published)
				return
			}
			assert.Empty(t, summary.Errors)
			assert.Len(t, summary.Actions, 1)
			assert.Len(t, notifier.Published, 1)
		})
	}
}

// 上限が未登録または負の場合、読み取りを含む AWS API を呼ばず起動を拒否する。
func TestEcsStartRejectsMissingOrInvalidLimitBeforeAPICalls(t *testing.T) {
	for _, limit := range []int32{0, -1} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			controller := gomock.NewController(t)
			target := &EcsServiceTarget{Ecs: mocks.NewMockEcsAPI(controller), AutoScaling: mocks.NewMockAutoScalingAPI(controller)}
			assert.ErrorContains(t, target.Start(context.Background(), model.Resource{Ref: "dev/api", EcsMaxCount: limit}), "ecs_max_count must be registered")
		})
	}
}

// 未登録または超過した上限を持つサービスでも、停止の更新値は 0 または 0/0 となる。
func TestEcsStopAllowsCountsAboveLimit(t *testing.T) {
	for _, limit := range []int32{0, 3} {
		for _, scalable := range []bool{false, true} {
			t.Run(fmt.Sprintf("limit=%d/scalable=%t", limit, scalable), func(t *testing.T) {
				controller := gomock.NewController(t)
				ecsClient := mocks.NewMockEcsAPI(controller)
				autoScaling := mocks.NewMockAutoScalingAPI(controller)
				out := &aas.DescribeScalableTargetsOutput{}
				if scalable {
					out.ScalableTargets = []aastypes.ScalableTarget{{MinCapacity: aws.Int32(1000), MaxCapacity: aws.Int32(1000)}}
					autoScaling.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(assertScalableBounds(t, 0, 0, true))
				} else {
					ecsClient.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
						func(_ context.Context, input *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
							assert.Zero(t, aws.ToInt32(input.DesiredCount))
							return &ecs.UpdateServiceOutput{}, nil
						})
				}
				autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(out, nil)
				resource := model.Resource{Ref: "dev/api", EcsMaxCount: limit, Tags: map[string]string{model.EcsDesiredCountTagKey: "1000"}}
				require.NoError(t, (&EcsServiceTarget{Ecs: ecsClient, AutoScaling: autoScaling}).Stop(context.Background(), resource))
			})
		}
	}
}
