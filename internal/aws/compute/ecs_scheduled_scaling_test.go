package compute

import (
	"context"
	"errors"
	"strconv"
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

	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/app/reconcile"
	"cheapskate/internal/aws/compute/mocks"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
)

// AWS の上下限による台数調整を mock で再現し、複数サイクルの設定維持と再適用を検証する。
type ecsScalingFixture struct {
	service        ecstypes.Service
	scalable       *aastypes.ScalableTarget
	ecs            *mocks.MockEcsAPI
	autoScaling    *mocks.MockAutoScalingAPI
	requests       []aas.RegisterScalableTargetInput
	updates        []int32
	convergeTasks  bool
	beforeRegister func(*aas.RegisterScalableTargetInput) error
	afterRegister  func(*aas.RegisterScalableTargetInput) error
}

func newEcsScalingFixture(t *testing.T, desired, minimum, maximum int32, paused bool) *ecsScalingFixture {
	t.Helper()
	controller := gomock.NewController(t)
	f := &ecsScalingFixture{
		convergeTasks: true,
		service:       ecstypes.Service{Status: aws.String("ACTIVE"), SchedulingStrategy: ecstypes.SchedulingStrategyReplica, DesiredCount: desired, RunningCount: desired},
		scalable: &aastypes.ScalableTarget{MinCapacity: aws.Int32(minimum), MaxCapacity: aws.Int32(maximum), SuspendedState: &aastypes.SuspendedState{
			ScheduledScalingSuspended: aws.Bool(paused), DynamicScalingInSuspended: aws.Bool(true), DynamicScalingOutSuspended: aws.Bool(true),
		}},
		ecs: mocks.NewMockEcsAPI(controller), autoScaling: mocks.NewMockAutoScalingAPI(controller),
	}
	f.ecs.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
			return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{f.service}}, nil
		})
	f.ecs.EXPECT().UpdateService(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(_ context.Context, input *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
			require.NotNil(t, input.DesiredCount)
			f.updates = append(f.updates, *input.DesiredCount)
			f.service.DesiredCount = *input.DesiredCount
			if f.convergeTasks {
				f.service.RunningCount = f.service.DesiredCount
				f.service.PendingCount = 0
			}
			return &ecs.UpdateServiceOutput{}, nil
		})
	f.autoScaling.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(context.Context, *aas.DescribeScalableTargetsInput, ...func(*aas.Options)) (*aas.DescribeScalableTargetsOutput, error) {
			out := &aas.DescribeScalableTargetsOutput{}
			if f.scalable != nil {
				out.ScalableTargets = []aastypes.ScalableTarget{*f.scalable}
			}
			return out, nil
		})
	f.autoScaling.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(_ context.Context, input *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
			require.NotNil(t, f.scalable, "an absent target must not be created")
			assert.Equal(t, aastypes.ServiceNamespaceEcs, input.ServiceNamespace)
			assert.Equal(t, "service/dev/api", aws.ToString(input.ResourceId))
			assert.Equal(t, scalableDimension, input.ScalableDimension)
			require.NotNil(t, input.SuspendedState)
			require.NotNil(t, input.SuspendedState.ScheduledScalingSuspended)
			assert.Nil(t, input.SuspendedState.DynamicScalingInSuspended)
			assert.Nil(t, input.SuspendedState.DynamicScalingOutSuspended)
			f.requests = append(f.requests, *input)
			if f.beforeRegister != nil {
				if err := f.beforeRegister(input); err != nil {
					return nil, err
				}
			}
			if input.MinCapacity != nil {
				f.scalable.MinCapacity = input.MinCapacity
			}
			if input.MaxCapacity != nil {
				f.scalable.MaxCapacity = input.MaxCapacity
			}
			if f.scalable.SuspendedState == nil {
				f.scalable.SuspendedState = &aastypes.SuspendedState{}
			}
			f.scalable.SuspendedState.ScheduledScalingSuspended = input.SuspendedState.ScheduledScalingSuspended
			f.service.DesiredCount = min(max(f.service.DesiredCount, aws.ToInt32(f.scalable.MinCapacity)), aws.ToInt32(f.scalable.MaxCapacity))
			if f.convergeTasks {
				f.service.RunningCount = f.service.DesiredCount
				f.service.PendingCount = 0
			}
			if f.afterRegister != nil {
				if err := f.afterRegister(input); err != nil {
					return nil, err
				}
			}
			return &aas.RegisterScalableTargetOutput{}, nil
		})
	return f
}

func (f *ecsScalingFixture) target() *EcsServiceTarget {
	return &EcsServiceTarget{Ecs: f.ecs, AutoScaling: f.autoScaling}
}

// AWS の一時停止状態に対応する上下限管理モードをテスト入力として返す。
func ecsBoundsModeForTest(paused bool) string {
	if paused {
		return "tags"
	}
	return "scheduled"
}

type ecsScalingStore struct{ override model.Override }

func (s ecsScalingStore) ListGroups(context.Context) ([]state.GroupRow, error) {
	return []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: s.override, EcsMaxCount: 3}}}, nil
}

func (f *ecsScalingFixture) cycle(t *testing.T, resource model.Resource, override model.Override, at time.Time) reconcile.Summary {
	t.Helper()
	deps := ecsReconcileDeps(resource, f.target(), &porttest.Notifier{})
	deps.Store = ecsScalingStore{override: override}
	summary, err := reconcile.Run(context.Background(), nil, deps, at)
	require.NoError(t, err)
	return summary
}

// 上下限管理モードと AWS 設定の差を観測し、scheduled モードでの scheduled action の変更は維持する。
func TestEcsDescribeScheduledScalingRepair(t *testing.T) {
	for _, test := range []struct {
		name                      string
		desired, minimum, maximum int32
		request, paused           bool
		nilState, nilFlag         bool
		wantStart, wantStop       bool
	}{
		{name: "scheduled bounds", desired: 5, minimum: 4, maximum: 6, wantStop: true},
		{name: "scheduled fixed bounds", desired: 4, minimum: 4, maximum: 4, wantStop: true},
		{name: "paused converged", desired: 3, minimum: 1, maximum: 3, request: true, paused: true, wantStop: true},
		{name: "pause missing", desired: 2, minimum: 1, maximum: 3, request: true, wantStart: true, wantStop: true},
		{name: "paused bounds changed", desired: 3, minimum: 2, maximum: 4, request: true, paused: true, wantStart: true, wantStop: true},
		{name: "resume requested", desired: 3, minimum: 1, maximum: 3, paused: true, wantStart: true, wantStop: true},
		{name: "stopped converged", paused: true, wantStart: true},
		{name: "stopped pause missing", wantStart: true, wantStop: true},
		{name: "nil suspended state", nilState: true, wantStart: true, wantStop: true},
		{name: "nil scheduled flag", nilFlag: true, wantStart: true, wantStop: true},
		{name: "draining stopped bounds", desired: 1, paused: true, wantStart: true},
		{name: "old startup count", desired: 4, minimum: 4, maximum: 4, paused: true, wantStart: true, wantStop: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newEcsScalingFixture(t, test.desired, test.minimum, test.maximum, test.paused)
			if test.nilState {
				f.scalable.SuspendedState = nil
			} else if test.nilFlag {
				f.scalable.SuspendedState.ScheduledScalingSuspended = nil
			}
			resource := ecsReconcileResource()
			resource.Tags[model.EcsScalingBoundsModeTagKey] = ecsBoundsModeForTest(test.request)
			observation, err := f.target().Describe(context.Background(), resource)
			require.NoError(t, err)
			assert.Equal(t, test.wantStart, observation.NeedsStart)
			assert.Equal(t, test.wantStop, observation.NeedsStop)
			assert.Contains(t, observation.Detail, "ScheduledScalingSuspended="+strconv.FormatBool(test.paused))
			assert.Contains(t, observation.Detail, "minCapacity="+strconv.Itoa(int(test.minimum)))
			assert.Empty(t, f.requests)
		})
	}
}

// tags モードで複数日の停止・起動を行い、通常範囲と AWS の一時停止を維持し、収束後には追加操作を行わない。
func TestEcsTagsBoundsModePersistsAcrossStopStartCycles(t *testing.T) {
	f := newEcsScalingFixture(t, 0, 0, 0, true)
	resource := ecsReconcileResource()
	resource.Tags[model.EcsScalingBoundsModeTagKey] = "tags"
	for day := 0; day < 3; day++ {
		at := time.Date(2026, 10, 4+day, 9, 0, 0, 0, time.UTC)
		for _, override := range []model.Override{model.OverrideRunning, model.OverrideStopped} {
			summary := f.cycle(t, resource, override, at)
			assert.Empty(t, summary.Errors)
			require.Len(t, summary.Actions, 1)
			assert.True(t, ecsScheduledScalingPaused(f.scalable))
			assert.Empty(t, f.cycle(t, resource, override, at.Add(time.Hour)).Actions)
		}
	}
	for _, input := range f.requests {
		assert.Equal(t, aws.Bool(true), input.SuspendedState.ScheduledScalingSuspended)
	}
	assert.Equal(t, "tags", resource.Tags[model.EcsScalingBoundsModeTagKey])
	assert.Empty(t, f.updates)
	assert.True(t, aws.ToBool(f.scalable.SuspendedState.DynamicScalingInSuspended))
	assert.True(t, aws.ToBool(f.scalable.SuspendedState.DynamicScalingOutSuspended))
}

// 稼働中の上下限を外部で変更して複数サイクル実行し、tags はタグ範囲へ戻し、scheduled は変更を維持する。
func TestEcsBoundsModesReconcileBoundsDrift(t *testing.T) {
	for _, test := range []struct {
		mode                          string
		paused                        bool
		wantDesired, wantMin, wantMax int32
		wantRequests                  int
	}{
		{mode: "tags", paused: true, wantDesired: 3, wantMin: 1, wantMax: 3, wantRequests: 1},
		{mode: "scheduled", wantDesired: 5, wantMin: 4, wantMax: 6},
	} {
		t.Run(test.mode, func(t *testing.T) {
			f := newEcsScalingFixture(t, 5, 4, 6, test.paused)
			resource := ecsReconcileResource()
			resource.Tags[model.EcsScalingBoundsModeTagKey] = test.mode
			at := time.Now()
			assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Errors)
			assert.Equal(t, test.wantDesired, f.service.DesiredCount)
			assert.True(t, ecsBoundsEqual(f.scalable, test.wantMin, test.wantMax))
			assert.Equal(t, test.paused, ecsScheduledScalingPaused(f.scalable))
			assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at.Add(time.Hour)).Actions)
			assert.Len(t, f.requests, test.wantRequests)
			assert.Empty(t, f.updates)
		})
	}
}

// scheduled モードで停止・起動し、停止中は一時停止し、起動設定を完了した後だけ Scheduled Scaling を再開する。
func TestEcsStopTemporarilySuspendsScheduledScaling(t *testing.T) {
	f := newEcsScalingFixture(t, 3, 1, 3, false)
	resource := ecsReconcileResource()
	resource.Tags[model.EcsScalingBoundsModeTagKey] = "scheduled"
	at := time.Now()
	assert.Empty(t, f.cycle(t, resource, model.OverrideStopped, at).Errors)
	require.Len(t, f.requests, 1)
	assert.True(t, ecsScheduledScalingPaused(f.scalable))
	assert.Zero(t, f.service.DesiredCount)
	assert.Empty(t, f.cycle(t, resource, model.OverrideStopped, at.Add(time.Hour)).Actions)
	assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at.Add(2*time.Hour)).Errors)
	require.Len(t, f.requests, 3)
	assert.Equal(t, aws.Bool(true), f.requests[1].SuspendedState.ScheduledScalingSuspended)
	assert.Equal(t, aws.Bool(false), f.requests[2].SuspendedState.ScheduledScalingSuspended)
	assert.EqualValues(t, 2, f.service.DesiredCount)
	assert.Equal(t, "scheduled", resource.Tags[model.EcsScalingBoundsModeTagKey])
	assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at.Add(3*time.Hour)).Actions)
}

// 稼働中に上下限管理モードを切り替え、通常の上下限を適用し、範囲内の現在台数を維持する。
func TestEcsRunningBoundsModeChangesPreserveDesiredCount(t *testing.T) {
	f := newEcsScalingFixture(t, 3, 2, 9, false)
	resource := ecsReconcileResource()
	at := time.Now()
	assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Actions)
	resource.Tags[model.EcsScalingBoundsModeTagKey] = "tags"
	assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Errors)
	require.Len(t, f.requests, 1)
	assert.Equal(t, aws.Int32(1), f.scalable.MinCapacity)
	assert.Equal(t, aws.Int32(3), f.scalable.MaxCapacity)
	assert.EqualValues(t, 3, f.service.DesiredCount)
	resource.Tags[model.EcsScalingBoundsModeTagKey] = "scheduled"
	assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Errors)
	require.Len(t, f.requests, 2)
	assert.Equal(t, aws.Int32(1), f.requests[1].MinCapacity)
	assert.Equal(t, aws.Int32(3), f.requests[1].MaxCapacity)
	assert.False(t, ecsScheduledScalingPaused(f.scalable))
	assert.EqualValues(t, 3, f.service.DesiredCount)
	assert.Empty(t, f.updates)
}

// 正の固定範囲に通常の上下限を適用し、起動台数のタグと異なる範囲内の現在台数を維持する。
func TestEcsStartAppliesNormalBoundsWithoutResettingFixedCapacity(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(strconv.FormatBool(paused), func(t *testing.T) {
			f := newEcsScalingFixture(t, 2, 2, 2, true)
			resource := ecsReconcileResource()
			resource.Tags[model.EcsScalingMinTagKey], resource.Tags[model.EcsScalingMaxTagKey] = "2", "2"
			resource.Tags[model.EcsScalingBoundsModeTagKey] = "tags"
			at := time.Now()
			assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Actions)
			resource.Tags[model.EcsDesiredCountTagKey] = "1"
			resource.Tags[model.EcsScalingMinTagKey], resource.Tags[model.EcsScalingMaxTagKey] = "1", "3"
			resource.Tags[model.EcsScalingBoundsModeTagKey] = ecsBoundsModeForTest(paused)
			assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Errors)
			assert.EqualValues(t, 2, f.service.DesiredCount)
			assert.Equal(t, aws.Int32(1), f.scalable.MinCapacity)
			assert.Equal(t, aws.Int32(3), f.scalable.MaxCapacity)
			assert.Equal(t, paused, ecsScheduledScalingPaused(f.scalable))
			assert.Empty(t, f.updates)
			assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Actions)
			assert.Len(t, f.requests, 1)
		})
	}
}

// 台数固定の応答喪失後は、起動待ちのタスクが残っていても通常設定へ収束し、起動台数を再設定しない。
func TestEcsLostPinResponseConvergesWhileTasksStart(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(strconv.FormatBool(paused), func(t *testing.T) {
			f := newEcsScalingFixture(t, 0, 0, 0, true)
			f.convergeTasks = false
			f.afterRegister = func(*aas.RegisterScalableTargetInput) error {
				f.afterRegister = nil
				f.service.PendingCount = 2
				return errors.New("pin response lost")
			}
			resource := ecsReconcileResource()
			resource.Tags[model.EcsScalingBoundsModeTagKey] = ecsBoundsModeForTest(paused)
			at := time.Now()
			require.Len(t, f.cycle(t, resource, model.OverrideRunning, at).Errors, 1)
			assert.EqualValues(t, 2, f.service.DesiredCount)
			assert.Zero(t, f.service.RunningCount)
			assert.EqualValues(t, 2, f.service.PendingCount)
			require.True(t, ecsBoundsEqual(f.scalable, 2, 2))
			assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Errors)
			assert.True(t, ecsBoundsEqual(f.scalable, 1, 3))
			assert.Equal(t, paused, ecsScheduledScalingPaused(f.scalable))
			assert.EqualValues(t, 2, f.service.PendingCount)
			for running := int32(0); running <= 2; running++ {
				f.service.RunningCount, f.service.PendingCount = running, 2-running
				assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Actions)
				assert.EqualValues(t, 2, f.service.DesiredCount)
			}
			assert.Len(t, f.requests, 2)
			assert.Empty(t, f.updates)
		})
	}
}

// 起動待ちのタスクが残る状態でも停止要求を優先し、タスク終了後に起動設定を復元しない。
func TestEcsStopDuringStartupPreservesStopRequest(t *testing.T) {
	f := newEcsScalingFixture(t, 2, 2, 2, true)
	f.convergeTasks = false
	f.service.RunningCount, f.service.PendingCount = 1, 1
	resource := ecsReconcileResource()
	at := time.Now()
	assert.Empty(t, f.cycle(t, resource, model.OverrideStopped, at).Errors)
	assert.Zero(t, f.service.DesiredCount)
	assert.EqualValues(t, 1, f.service.RunningCount)
	assert.EqualValues(t, 1, f.service.PendingCount)
	assert.True(t, ecsBoundsEqual(f.scalable, 0, 0))
	assert.True(t, ecsScheduledScalingPaused(f.scalable))
	f.service.RunningCount, f.service.PendingCount = 0, 0
	assert.Empty(t, f.cycle(t, resource, model.OverrideStopped, at).Actions)
	assert.Len(t, f.requests, 1)
	assert.Empty(t, f.updates)
}

// 通常範囲も起動台数と同じ場合、更新失敗後の再適用で必要なフラグだけを収束させる。
func TestEcsFixedStartupBoundsRecoverSuspension(t *testing.T) {
	for _, paused := range []bool{false, true} {
		for _, failure := range []string{"pin", "pin response lost", "finish", "finish response lost"} {
			if paused && (failure == "finish" || failure == "finish response lost") {
				continue
			}
			t.Run(failure+"/paused="+strconv.FormatBool(paused), func(t *testing.T) {
				f := newEcsScalingFixture(t, 0, 0, 0, true)
				fail := func(input *aas.RegisterScalableTargetInput) error {
					pin := aws.ToBool(input.SuspendedState.ScheduledScalingSuspended)
					if (pin && (failure == "pin" || failure == "pin response lost")) || (!pin && (failure == "finish" || failure == "finish response lost")) {
						f.beforeRegister, f.afterRegister = nil, nil
						return errors.New("temporary fixed-bounds failure")
					}
					return nil
				}
				if failure == "pin response lost" || failure == "finish response lost" {
					f.afterRegister = fail
				} else {
					f.beforeRegister = fail
				}
				resource := ecsReconcileResource()
				resource.Tags[model.EcsScalingMinTagKey], resource.Tags[model.EcsScalingMaxTagKey] = "2", "2"
				resource.Tags[model.EcsScalingBoundsModeTagKey] = ecsBoundsModeForTest(paused)
				at := time.Now()
				require.Len(t, f.cycle(t, resource, model.OverrideRunning, at).Errors, 1)
				assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Errors)
				assert.EqualValues(t, 2, f.service.DesiredCount)
				assert.True(t, ecsBoundsEqual(f.scalable, 2, 2))
				assert.Equal(t, paused, ecsScheduledScalingPaused(f.scalable))
				before := len(f.requests)
				assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Actions)
				assert.Len(t, f.requests, before)
			})
		}
	}
}

// 停止設定の一時停止を補い、稼働中の解除では通常範囲を適用して範囲外の設定台数を調整する。
func TestEcsRepairsLegacyStopAndResumesWithNormalBounds(t *testing.T) {
	resource := ecsReconcileResource()
	f := newEcsScalingFixture(t, 0, 0, 0, false)
	at := time.Now()
	summary := f.cycle(t, resource, model.OverrideStopped, at)
	assert.Empty(t, summary.Errors)
	require.Len(t, summary.Actions, 1)
	assert.Equal(t, model.ActionStop, summary.Actions[0].Action)
	assert.True(t, ecsScheduledScalingPaused(f.scalable))
	assert.Empty(t, f.updates)

	f = newEcsScalingFixture(t, 5, 4, 6, true)
	f.convergeTasks = false
	summary = f.cycle(t, resource, model.OverrideRunning, at)
	assert.Empty(t, summary.Errors)
	require.Len(t, summary.Actions, 1)
	require.Len(t, f.requests, 1)
	assert.Equal(t, aws.Int32(1), f.requests[0].MinCapacity)
	assert.Equal(t, aws.Int32(3), f.requests[0].MaxCapacity)
	assert.EqualValues(t, 3, f.service.DesiredCount)
	assert.EqualValues(t, 5, f.service.RunningCount)
	assert.True(t, ecsBoundsEqual(f.scalable, 1, 3))
	assert.False(t, ecsScheduledScalingPaused(f.scalable))
	assert.Empty(t, f.updates)
	assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Actions)
	f.service.RunningCount = 3
	assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Actions)
	assert.Len(t, f.requests, 1)
}

// 最後の更新の応答喪失後に scheduled action が上下限を変更しても、その結果を初期値へ戻さない。
func TestEcsLostResumeResponsePreservesSubsequentScheduledBounds(t *testing.T) {
	f := newEcsScalingFixture(t, 0, 0, 0, true)
	f.afterRegister = func(input *aas.RegisterScalableTargetInput) error {
		if !aws.ToBool(input.SuspendedState.ScheduledScalingSuspended) {
			f.afterRegister = nil
			return errors.New("resume response lost")
		}
		return nil
	}
	resource := ecsReconcileResource()
	at := time.Now()
	require.Len(t, f.cycle(t, resource, model.OverrideRunning, at).Errors, 1)
	f.scalable.MinCapacity, f.scalable.MaxCapacity = aws.Int32(4), aws.Int32(6)
	f.service.DesiredCount, f.service.RunningCount = 5, 5
	before := len(f.requests)
	summary := f.cycle(t, resource, model.OverrideRunning, at.Add(time.Hour))
	assert.Empty(t, summary.Errors)
	assert.Empty(t, summary.Actions)
	assert.Len(t, f.requests, before)
	assert.EqualValues(t, 5, f.service.DesiredCount)
}

// 未適用と応答喪失の停止失敗から再試行し、0/0 と一時停止が適用済みなら追加の更新を抑止する。
func TestEcsStopFailureConvergesWithoutRestoringScaling(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(strconv.FormatBool(applied), func(t *testing.T) {
			f := newEcsScalingFixture(t, 2, 1, 3, false)
			fail := func(*aas.RegisterScalableTargetInput) error {
				f.beforeRegister, f.afterRegister = nil, nil
				return errors.New("stop failed")
			}
			if applied {
				f.afterRegister = fail
			} else {
				f.beforeRegister = fail
			}
			resource := ecsReconcileResource()
			at := time.Now()
			require.Len(t, f.cycle(t, resource, model.OverrideStopped, at).Errors, 1)
			assert.Empty(t, f.cycle(t, resource, model.OverrideStopped, at).Errors)
			assert.True(t, ecsBoundsEqual(f.scalable, 0, 0))
			assert.True(t, ecsScheduledScalingPaused(f.scalable))
			wantRequests := 2
			if applied {
				wantRequests = 1
			}
			assert.Len(t, f.requests, wantRequests)
		})
	}
}

// 古いモードでの再適用後も最新のモードへ収束し、disabled は適用済みの AWS 設定を変更しない。
func TestEcsLatestBoundsModeConvergesAfterStaleConfiguration(t *testing.T) {
	f := newEcsScalingFixture(t, 2, 1, 3, false)
	resource := ecsReconcileResource()
	resource.Tags[model.EcsScalingBoundsModeTagKey] = "tags"
	at := time.Now()
	assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Errors)
	stale := ecsReconcileResource()
	require.NoError(t, f.target().Start(context.Background(), stale))
	assert.False(t, ecsScheduledScalingPaused(f.scalable))
	assert.Empty(t, f.cycle(t, resource, model.OverrideRunning, at).Errors)
	assert.True(t, ecsScheduledScalingPaused(f.scalable))
	before := len(f.requests)
	assert.Empty(t, f.cycle(t, stale, model.OverrideDisabled, at).Actions)
	assert.Len(t, f.requests, before)
	assert.True(t, ecsScheduledScalingPaused(f.scalable))
}

// scalable target がない場合も上下限管理モードを検証し、target を作らず ECS の起動・停止だけを行う。
func TestEcsBoundsModeWithoutScalableTarget(t *testing.T) {
	f := newEcsScalingFixture(t, 0, 0, 0, false)
	f.scalable = nil
	resource := ecsReconcileResource()
	resource.Tags[model.EcsScalingBoundsModeTagKey] = "tags"
	require.NoError(t, f.target().Start(context.Background(), resource))
	require.NoError(t, f.target().Stop(context.Background(), resource))
	assert.Equal(t, []int32{2, 0}, f.updates)
	assert.Empty(t, f.requests)
	resource.Tags[model.EcsScalingBoundsModeTagKey] = "invalid"
	_, err := f.target().Describe(context.Background(), resource)
	assert.ErrorContains(t, err, model.EcsScalingBoundsModeTagKey)
	assert.Error(t, f.target().Start(context.Background(), resource))
	assert.Error(t, f.target().Stop(context.Background(), resource))
	assert.Len(t, f.updates, 2)
}

// 観測後に起動済みとなった target 未登録の service は、古い Start 呼び出しで起動台数へ戻さない。
func TestEcsStartWithoutScalableTargetPreservesActiveService(t *testing.T) {
	for _, service := range []ecstypes.Service{
		{DesiredCount: 3, RunningCount: 3},
		{DesiredCount: 2, PendingCount: 2},
		{RunningCount: 1},
	} {
		t.Run(strconv.Itoa(int(service.DesiredCount))+"/"+strconv.Itoa(int(service.RunningCount)), func(t *testing.T) {
			f := newEcsScalingFixture(t, service.DesiredCount, 0, 0, false)
			f.scalable = nil
			f.service.RunningCount, f.service.PendingCount = service.RunningCount, service.PendingCount
			require.NoError(t, f.target().Start(context.Background(), ecsReconcileResource()))
			assert.Equal(t, service.DesiredCount, f.service.DesiredCount)
			assert.Empty(t, f.updates)
			assert.Empty(t, f.requests)
		})
	}
}
