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

// 指定の desiredCount を返す DescribeServices 呼び出し 1 回分の EXPECT を設定する
func ecsDescribe(e *mocks.MockEcsAPI, desiredCount int32) {
	e.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
		Status:       aws.String("ACTIVE"),
		DesiredCount: desiredCount,
	}}}, nil)
}

// target を返す DescribeScalableTargets 呼び出し 1 回分の EXPECT を設定する (nil は該当なしを表す)
func aasDescribe(a *mocks.MockAutoScalingAPI, target *aastypes.ScalableTarget) {
	out := &aas.DescribeScalableTargetsOutput{}
	if target != nil {
		out.ScalableTargets = []aastypes.ScalableTarget{*target}
	}
	a.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(out, nil)
}

func TestEcsStopPinsScalingAndZeroesDesiredCount(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(2)), MaxCapacity: new(int32(6))})
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

	require.NoError(t, tgt.Stop(context.Background(), "dev/api"))
}

func TestEcsStopWithoutScalingTarget(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, nil)
	// RegisterScalableTarget の EXPECT は設定しない
	// スケーリングターゲットが存在しない場合、登録してはならないためである
	e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).Return(&ecs.UpdateServiceOutput{}, nil)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	require.NoError(t, tgt.Stop(context.Background(), "dev/api"))
}

func TestEcsStartUsesTagsForCountAndScaling(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(0)), MaxCapacity: new(int32(0))})
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
	res := model.Resource{Ref: "dev/api", Tags: map[string]string{
		model.EcsDesiredCountTagKey: "3",
		model.EcsScalingMinTagKey:   "2",
		model.EcsScalingMaxTagKey:   "6",
	}}

	require.NoError(t, tgt.Start(context.Background(), res))
}

func TestEcsStartDefaultsCountToOneWithoutTag(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, nil) // スケーラブルターゲットが存在しないため RegisterScalableTarget を呼んではならない
	e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
			assert.Equal(t, int32(1), *in.DesiredCount)
			return &ecs.UpdateServiceOutput{}, nil
		})
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	require.NoError(t, tgt.Start(context.Background(), model.Resource{Ref: "dev/api"}))
}

func TestEcsStartDefaultsScalingBoundsToDesiredCount(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(0)), MaxCapacity: new(int32(0))})
	a.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
			assert.Equal(t, int32(4), *in.MinCapacity)
			assert.Equal(t, int32(4), *in.MaxCapacity)
			return &aas.RegisterScalableTargetOutput{}, nil
		})
	e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).Return(&ecs.UpdateServiceOutput{}, nil)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}
	res := model.Resource{Ref: "dev/api", Tags: map[string]string{model.EcsDesiredCountTagKey: "4"}}

	require.NoError(t, tgt.Start(context.Background(), res))
}

func TestEcsStartRejectsBadCountTag(t *testing.T) {
	// API の EXPECT は設定しない
	// 不正な desired-count タグは、AWS の呼び出し前に失敗しなければならないためである
	cases := map[string]string{
		"non-integer": "abc",
		"zero":        "0",
		"negative":    "-1",
	}
	for name, v := range cases {
		ctrl := gomock.NewController(t)
		e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
		tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}
		res := model.Resource{Ref: "dev/api", Tags: map[string]string{model.EcsDesiredCountTagKey: v}}
		assert.Error(t, tgt.Start(context.Background(), res), name)
	}
}

func TestEcsStartRejectsScalingMinAboveMax(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(0)), MaxCapacity: new(int32(0))})
	// RegisterScalableTarget と UpdateService の EXPECT は設定しない
	// 不正な上下限は、変更の呼び出し前に失敗しなければならないためである
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}
	res := model.Resource{Ref: "dev/api", Tags: map[string]string{
		model.EcsDesiredCountTagKey: "3",
		model.EcsScalingMinTagKey:   "5",
		model.EcsScalingMaxTagKey:   "4",
	}}

	require.Error(t, tgt.Start(context.Background(), res))
}

// min > max と同一の不等式に対する検査である
// desired-count が上下限の外にある設定を通した場合、UpdateService による変更の直後に Auto Scaling が上下限まで引き戻すため、指定した台数は実現しない
// TestEcsStartRejectsScalingMinAboveMax と同じく、変更の呼び出し前に失敗しなければならない
func TestEcsStartRejectsDesiredCountOutsideScalingBounds(t *testing.T) {
	cases := map[string]map[string]string{
		"above max": {
			model.EcsDesiredCountTagKey: "10",
			model.EcsScalingMinTagKey:   "1",
			model.EcsScalingMaxTagKey:   "4",
		},
		"below min": {
			model.EcsDesiredCountTagKey: "1",
			model.EcsScalingMinTagKey:   "3",
			model.EcsScalingMaxTagKey:   "6",
		},
		"above max with min defaulted": {
			model.EcsDesiredCountTagKey: "10",
			model.EcsScalingMaxTagKey:   "4",
		},
	}
	for name, tags := range cases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
			aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(0)), MaxCapacity: new(int32(0))})
			// RegisterScalableTarget と UpdateService の EXPECT は設定しない
			tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

			err := tgt.Start(context.Background(), model.Resource{Ref: "dev/api", Tags: tags})

			require.Error(t, err)
			assert.Contains(t, err.Error(), model.EcsDesiredCountTagKey, "どのタグを直せばよいか分かる文言でなければならない")
		})
	}
}

// 上下限の内側にある desired-count は、そのまま通す
// スケーラブルターゲットを持つサービスにおいて、min < desired < max は妥当な設定である
func TestEcsStartAcceptsDesiredCountInsideScalingBounds(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(0)), MaxCapacity: new(int32(0))})
	a.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
			assert.Equal(t, int32(1), *in.MinCapacity)
			assert.Equal(t, int32(6), *in.MaxCapacity)
			return &aas.RegisterScalableTargetOutput{}, nil
		})
	e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
			assert.Equal(t, int32(3), *in.DesiredCount)
			return &ecs.UpdateServiceOutput{}, nil
		})
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}
	res := model.Resource{Ref: "dev/api", Tags: map[string]string{
		model.EcsDesiredCountTagKey: "3",
		model.EcsScalingMinTagKey:   "1",
		model.EcsScalingMaxTagKey:   "6",
	}}

	require.NoError(t, tgt.Start(context.Background(), res))
}

func TestEcsDescribeStates(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	ecsDescribe(e, 2)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}
	obs, err := tgt.Describe(context.Background(), "dev/api")
	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, obs.State)
	assert.Equal(t, "desiredCount=2", obs.Detail, "種別固有の細部は Detail が運ぶ")

	e2 := mocks.NewMockEcsAPI(ctrl)
	ecsDescribe(e2, 0)
	tgt = &EcsServiceTarget{Ecs: e2, AutoScaling: a}
	obs, err = tgt.Describe(context.Background(), "dev/api")
	require.NoError(t, err)
	assert.Equal(t, model.StateStopped, obs.State)

	_, err = tgt.Describe(context.Background(), "noslash")
	require.Error(t, err, "want error for malformed ecs ref")
}

// 削除済みのサービスと INACTIVE なサービスは、StateNotFound とする
// reconcile はこれをスキップとして扱い、通知と status の書き込みのいずれも行わない
// rds-instance、rds-cluster、ec2-instance と同じ規約であり、ecs-service を例外としない
func TestEcsDescribeNotFound(t *testing.T) {
	cases := map[string]*ecs.DescribeServicesOutput{
		"no services returned":  {},
		"service is INACTIVE":   {Services: []ecstypes.Service{{Status: aws.String("INACTIVE"), DesiredCount: 2}}},
		"service has no status": {Services: []ecstypes.Service{{DesiredCount: 2}}},
	}
	for name, out := range cases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
			e.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(out, nil)
			tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

			obs, err := tgt.Describe(context.Background(), "dev/api")

			require.NoError(t, err, "存在しないことはエラーではない")
			assert.Equal(t, model.StateNotFound, obs.State)
			assert.Empty(t, obs.Detail, "実在しないサービスの desiredCount を報告してはならない")
		})
	}
}

// AWS 側のエラーは、not-found として扱ってはならない
// StateNotFound を返した場合、reconcile はスキップし、収束していない事実が通知にもステータスにも現れない
func TestEcsDescribeErrorPassesThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	e.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	_, err := tgt.Describe(context.Background(), "dev/api")

	assert.ErrorIs(t, err, assert.AnError)
}

// ref は "<cluster>/<service>" でなければならない
// 解析できないまま処理を続けた場合、cluster が空となり、既定クラスタの同名サービスを操作しうる
// Describe と同じく、Stop と Start も AWS を呼ばずに失敗する
func TestEcsStopStartRejectMalformedRef(t *testing.T) {
	for _, name := range []string{"stop", "start"} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			// API の EXPECT は設定しない
			tgt := &EcsServiceTarget{Ecs: mocks.NewMockEcsAPI(ctrl), AutoScaling: mocks.NewMockAutoScalingAPI(ctrl)}

			var err error
			if name == "stop" {
				err = tgt.Stop(context.Background(), "noslash")
			} else {
				err = tgt.Start(context.Background(), model.Resource{Ref: "noslash"})
			}

			assert.Error(t, err, "want error for malformed ecs ref")
		})
	}
}

// スケーラブルターゲットを読めない場合、0/0 への変更の可否も元の値への復元の可否も判定できない
// stop と start のいずれの場合も処理を中断し、UpdateService を呼んではならない
func TestEcsDescribeScalableTargetsErrorAbortsWithoutTouchingService(t *testing.T) {
	for _, name := range []string{"stop", "start"} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
			a.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
			// UpdateService と RegisterScalableTarget の EXPECT は設定しない
			tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

			var err error
			if name == "stop" {
				err = tgt.Stop(context.Background(), "dev/api")
			} else {
				err = tgt.Start(context.Background(), model.Resource{Ref: "dev/api"})
			}

			assert.ErrorIs(t, err, assert.AnError)
		})
	}
}

// スケーラブルターゲットの登録が失敗した場合は、処理を中断する
// stop において 0/0 への変更を行わずに desiredCount のみを 0 とした場合、Auto Scaling が値を戻す
// start において min を戻さずに desiredCount のみを上げた場合、Auto Scaling が値を下げる
func TestEcsRegisterScalableTargetFailureAbortsBeforeUpdateService(t *testing.T) {
	for _, name := range []string{"stop", "start"} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
			aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(2)), MaxCapacity: new(int32(6))})
			a.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
			// UpdateService の EXPECT は設定しない
			tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

			var err error
			if name == "stop" {
				err = tgt.Stop(context.Background(), "dev/api")
			} else {
				err = tgt.Start(context.Background(), model.Resource{Ref: "dev/api"})
			}

			assert.ErrorIs(t, err, assert.AnError)
		})
	}
}

// desired-count と同じく、min/max のタグも解釈できない場合は既定値へ倒さずエラーとする
// これらは手作業で付与する値であり、誤記が意図しない台数での起動を招いてはならないためである
func TestEcsStartRejectsBadScalingBoundTags(t *testing.T) {
	cases := map[string]map[string]string{
		"non-integer min": {model.EcsScalingMinTagKey: "two"},
		"non-integer max": {model.EcsScalingMaxTagKey: "lots"},
		"negative min":    {model.EcsScalingMinTagKey: "-1"},
		"negative max":    {model.EcsScalingMaxTagKey: "-1"},
	}
	for name, tags := range cases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
			aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(0)), MaxCapacity: new(int32(0))})
			// RegisterScalableTarget と UpdateService の EXPECT は設定しない
			tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

			err := tgt.Start(context.Background(), model.Resource{Ref: "dev/api", Tags: tags})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "cheapskate/scaling-", "どのタグが悪いのか分かる文言でなければならない")
		})
	}
}

// stop は原子的ではない
// スケーラブルターゲットを 0/0 とした後に UpdateService が失敗した場合、サービスは起動したまま
// Auto Scaling が 0/0 に固定された状態で残り、スケールアウトが不可能となる
// 元の min/max は DescribeScalableTargets が返しているため、その値で巻き戻す
func TestEcsStopRollsBackScalingWhenUpdateServiceFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(2)), MaxCapacity: new(int32(6))})

	var registered [][2]int32
	a.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).Times(2).DoAndReturn(
		func(_ context.Context, in *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
			registered = append(registered, [2]int32{*in.MinCapacity, *in.MaxCapacity})
			return &aas.RegisterScalableTargetOutput{}, nil
		})
	e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	err := tgt.Stop(context.Background(), "dev/api")

	require.Error(t, err, "a rolled-back stop is still a failed stop")
	assert.Equal(t, [][2]int32{{0, 0}, {2, 6}}, registered, "the scalable target must end up back at its original bounds")
	assert.Contains(t, err.Error(), "rolled back to 2/6")
}

// 巻き戻しも失敗した場合は、0/0 のまま残存していることと復元すべき値をエラー本文へ含める
// この本文が status# の last_error と SNS 通知に現れる
func TestEcsStopReportsClampedTargetWhenRollbackAlsoFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(1)), MaxCapacity: new(int32(4))})
	gomock.InOrder(
		a.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).Return(&aas.RegisterScalableTargetOutput{}, nil),
		a.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).Return(nil, assert.AnError),
	)
	e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	err := tgt.Stop(context.Background(), "dev/api")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "left clamped at 0/0")
	assert.Contains(t, err.Error(), "1/4", "the operator needs the values to restore by hand")
}

// スケーラブルターゲットが存在しない場合、0/0 とした対象も復元先も存在しない
// UpdateService のエラーをそのまま返し、RegisterScalableTarget を呼んではならない
func TestEcsStopWithoutScalingTargetDoesNotRollBack(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, nil)
	e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	assert.ErrorIs(t, tgt.Stop(context.Background(), "dev/api"), assert.AnError)
}
