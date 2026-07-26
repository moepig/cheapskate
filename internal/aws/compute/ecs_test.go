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

// 指定の desiredCount を返す DescribeServices 呼び出し 1 回分の EXPECT を仕込む
func ecsDescribe(e *mocks.MockEcsAPI, desiredCount int32) {
	e.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
		Status:       aws.String("ACTIVE"),
		DesiredCount: desiredCount,
	}}}, nil)
}

// target を返す DescribeScalableTargets 呼び出し 1 回分の EXPECT を仕込む（nil なら該当なし）
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
	// RegisterScalableTarget の EXPECT はない
	// スケーリングターゲットがなければ登録してはならないためである
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
	aasDescribe(a, nil) // スケーラブルターゲットがないので RegisterScalableTarget は呼ばれてはならない
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
	// API の EXPECT はない
	// 不正な desired-count タグは AWS 呼び出しの前に失敗しなければならないためである
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
	// RegisterScalableTarget と UpdateService の EXPECT はない
	// 不正な上下限は変更呼び出しの前に失敗しなければならないためである
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}
	res := model.Resource{Ref: "dev/api", Tags: map[string]string{
		model.EcsDesiredCountTagKey: "3",
		model.EcsScalingMinTagKey:   "5",
		model.EcsScalingMaxTagKey:   "4",
	}}

	require.Error(t, tgt.Start(context.Background(), res))
}

// min > max の兄弟にあたる検査
// desired-count が上下限の外にある設定は、通せば「UpdateService でその台数にした直後に Auto Scaling が上下限まで引き戻す」を必ず招き、オペレータの指定した台数が黙って実現しない
// TestEcsStartRejectsScalingMinAboveMax と同じく、変更呼び出しの前に失敗しなければならない
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
			// RegisterScalableTarget と UpdateService の EXPECT はない
			tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

			err := tgt.Start(context.Background(), model.Resource{Ref: "dev/api", Tags: tags})

			require.Error(t, err)
			assert.Contains(t, err.Error(), model.EcsDesiredCountTagKey, "どのタグを直せばよいか分かる文言でなければならない")
		})
	}
}

// 上下限の内側にある desired-count はそのまま通す
// スケーラブルターゲットを持つサービスで min < desired < max は普通の設定である
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

// 消されたサービスや INACTIVE なサービスは StateNotFound へ落とす
// reconcile はこれを穏当なスキップとして扱い、通知も status の書き込みも行わない
// rds-instance / rds-cluster / ec2-instance と同じ約束であり、ecs-service だけ例外にはできない
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

// AWS 側のエラー（権限不足、スロットリングなど）は「見つからない」に丸めてはならない
// StateNotFound を返すと reconcile が黙ってスキップし、収束していない事実が誰にも届かなくなる
func TestEcsDescribeErrorPassesThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	e.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	_, err := tgt.Describe(context.Background(), "dev/api")

	assert.ErrorIs(t, err, assert.AnError)
}

// ref は "<cluster>/<service>" でなければならない
// 解析できないまま進むと、cluster を空にしたまま既定クラスタの同名サービスを操作しかねない
// Describe と同じく、Stop / Start も AWS を一切呼ばずに失敗する
func TestEcsStopStartRejectMalformedRef(t *testing.T) {
	for _, name := range []string{"stop", "start"} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			// API の EXPECT はない
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

// スケーラブルターゲットを読めなければ、0/0 に潰してよいかも元の値へ戻せるかも分からない
// 推測して進むのではなく、どちらの向きでも中断して UpdateService を呼んではならない
func TestEcsDescribeScalableTargetsErrorAbortsWithoutTouchingService(t *testing.T) {
	for _, name := range []string{"stop", "start"} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
			a.EXPECT().DescribeScalableTargets(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
			// UpdateService と RegisterScalableTarget の EXPECT はない
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

// スケーラブルターゲットの登録が失敗したら、そこで止める
// stop で 0/0 に潰せていないのに desiredCount だけ 0 にすると Auto Scaling がすぐ戻し、start で min を戻せていないのに desiredCount だけ上げると Auto Scaling がすぐ潰す
func TestEcsRegisterScalableTargetFailureAbortsBeforeUpdateService(t *testing.T) {
	for _, name := range []string{"stop", "start"} {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
			aasDescribe(a, &aastypes.ScalableTarget{MinCapacity: new(int32(2)), MaxCapacity: new(int32(6))})
			a.EXPECT().RegisterScalableTarget(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
			// UpdateService の EXPECT はない
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

// desired-count と同じく、min/max のタグも解釈できなければ黙って既定値へ倒さずエラーにする
// これらはオペレータが手で付ける値なので、打ち間違いが「意図しない台数で起動する」に化けてはならない
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
			// RegisterScalableTarget と UpdateService の EXPECT はない
			tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

			err := tgt.Start(context.Background(), model.Resource{Ref: "dev/api", Tags: tags})

			require.Error(t, err)
			assert.Contains(t, err.Error(), "cheapskate/scaling-", "どのタグが悪いのか分かる文言でなければならない")
		})
	}
}

// stop は原子的ではない
// スケーラブルターゲットを 0/0 にしたあとで UpdateService が失敗すると、サービスは起動したまま
// Auto Scaling だけが 0/0 に固定されて残り、スケールアウトできなくなる
// 元の min/max は DescribeScalableTargets がすでに返しているので、その値で巻き戻す
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

// 巻き戻しにも失敗したら、0/0 のまま残っていることと戻すべき値をエラー本文で伝える
// これがそのまま status# の last_error と SNS 通知になる
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

// スケーラブルターゲットがなければ 0/0 にしたものもないので、巻き戻す先もない
// UpdateService のエラーをそのまま返し、余計な RegisterScalableTarget を呼んではならない
func TestEcsStopWithoutScalingTargetDoesNotRollBack(t *testing.T) {
	ctrl := gomock.NewController(t)
	e, a := mocks.NewMockEcsAPI(ctrl), mocks.NewMockAutoScalingAPI(ctrl)
	aasDescribe(a, nil)
	e.EXPECT().UpdateService(gomock.Any(), gomock.Any()).Return(nil, assert.AnError)
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	assert.ErrorIs(t, tgt.Stop(context.Background(), "dev/api"), assert.AnError)
}
