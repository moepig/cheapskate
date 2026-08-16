package compute

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"cheapskate/internal/core/model"
)

const scalableDimension = aastypes.ScalableDimensionECSServiceDesiredCount

// ターゲットが使う ECS クライアントの部分集合
type EcsAPI interface {
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, opts ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
}

// 利用する Application Auto Scaling クライアントの部分集合
type AutoScalingAPI interface {
	DescribeScalableTargets(ctx context.Context, in *aas.DescribeScalableTargetsInput, opts ...func(*aas.Options)) (*aas.DescribeScalableTargetsOutput, error)
	RegisterScalableTarget(ctx context.Context, in *aas.RegisterScalableTargetInput, opts ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error)
}

// stop は desiredCount を 0 とし、start はリソース自身の model.EcsDesiredCountTagKey タグから desiredCount を取得する (未設定の場合は 1)
//
// サービスが Application Auto Scaling のターゲットを持つ場合、stop 時にその min/max を 0/0 とする
// これを行わない場合、スケーリングポリシーが desiredCount の変更を取り消す
// start 時は model.EcsScalingMinTagKey と EcsScalingMaxTagKey から書き戻す (未設定の場合は desiredCount を既定値とする)
type EcsServiceTarget struct {
	Ecs         EcsAPI
	AutoScaling AutoScalingAPI
}

func (t *EcsServiceTarget) Type() model.ResourceType { return model.TypeEcsService }

func (t *EcsServiceTarget) Describe(ctx context.Context, ref string) (model.Observation, error) {
	cluster, service, err := splitEcsRef(ref)
	if err != nil {
		return model.Observation{}, err
	}
	out, err := t.Ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: &cluster, Services: []string{service}})
	if err != nil {
		return model.Observation{}, err
	}
	for _, s := range out.Services {
		if s.Status != nil && *s.Status == "ACTIVE" {
			state := model.StateStopped
			if s.DesiredCount > 0 {
				state = model.StateRunning
			}
			return model.Observation{
				State:  state,
				Detail: fmt.Sprintf("desiredCount=%d", s.DesiredCount),
			}, nil
		}
	}
	return model.Observation{State: model.StateNotFound}, nil
}

// stop はスケーラブルターゲットの 0/0 化と desiredCount の 0 化の 2 段階からなり、原子的ではない
// 後段のみが失敗した場合、サービスは起動したまま Auto Scaling が 0/0 に固定された状態で残る
// この状態はスケールアウトが不可能であり、かつ停止の失敗を示すエラーからは判別できない
// したがって、後段が失敗した場合は前段を巻き戻す
func (t *EcsServiceTarget) Stop(ctx context.Context, ref string) error {
	cluster, service, err := splitEcsRef(ref)
	if err != nil {
		return err
	}
	scalable, err := t.scalableTarget(ctx, cluster, service)
	if err != nil {
		return err
	}
	if scalable != nil {
		if err := t.register(ctx, cluster, service, 0, 0); err != nil {
			return err
		}
	}
	var zero int32
	if _, err := t.Ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: &cluster, Service: &service, DesiredCount: &zero}); err != nil {
		if scalable == nil {
			return err // 0/0 とした対象が存在しないため、巻き戻す対象も存在しない
		}
		return t.rollbackFailedStop(ctx, cluster, service, scalable, err)
	}
	return nil
}

// 0/0 としたスケーラブルターゲットを、DescribeScalableTargets が返した元の min/max へ戻す
// 元の値は scalableTarget が返す ScalableTarget から取得できるため、追加の API 呼び出しと IAM 権限を必要としない
// この関数は必ずエラーを返す
// 巻き戻しの成否によらず停止自体は失敗しており、呼び出し側へその事実を伝える必要があるためである
// 巻き戻しも失敗した場合は、0/0 のまま手作業による復旧が必要であることをエラー本文へ含める
// この本文が status# の last_error と SNS 通知に現れる
func (t *EcsServiceTarget) rollbackFailedStop(ctx context.Context, cluster, service string, prev *aastypes.ScalableTarget, cause error) error {
	minimum, maximum := aws.ToInt32(prev.MinCapacity), aws.ToInt32(prev.MaxCapacity)
	if rerr := t.register(ctx, cluster, service, minimum, maximum); rerr != nil {
		return fmt.Errorf("stop failed: %w; scalable target is left clamped at 0/0 and must be restored to %d/%d by hand (rollback failed: %v)",
			cause, minimum, maximum, rerr)
	}
	return fmt.Errorf("stop failed: %w; scalable target rolled back to %d/%d", cause, minimum, maximum)
}

// start も 2 段階からなるが、Stop と異なり巻き戻しを行わない
// 前段で min が 1 以上へ戻った時点で Auto Scaling がサービスを起動するため、後段の UpdateService が失敗しても、残る状態は起動の方向にある
// 次のサイクルの再試行が desiredCount を目的の値へ揃える
func (t *EcsServiceTarget) Start(ctx context.Context, res model.Resource) error {
	cluster, service, err := splitEcsRef(res.Ref)
	if err != nil {
		return err
	}
	count, err := desiredCountFromTags(res.Tags)
	if err != nil {
		return err
	}
	scalable, err := t.scalableTarget(ctx, cluster, service)
	if err != nil {
		return err
	}
	if scalable != nil {
		minimum, maximum, err := scalingBoundsFromTags(res.Tags, count)
		if err != nil {
			return err
		}
		if err := t.register(ctx, cluster, service, minimum, maximum); err != nil {
			return err
		}
	}
	_, err = t.Ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: &cluster, Service: &service, DesiredCount: &count})
	return err
}

// ecs-service の Ref を、ECS API が個別の引数として要求する cluster と service へ分解する
// この分解を必要とするのは ECS API の呼び出し側に限るため、Ref の文法を宣言するドメイン (model の ecs-service の RefPattern) ではなく、ここに置く
//
// 探索を通ったリソースの Ref は、その文法で検証済みである (model.Resource.Validate)
// ここで再度検証するのは、cluster または service が空のまま DescribeServices や UpdateService を呼ばないためである
// ECS は空文字のクラスタ名を default クラスタとして解釈するため、意図しないクラスタへ操作が及びうる
func splitEcsRef(ref string) (cluster, service string, err error) {
	cluster, service, found := strings.Cut(ref, "/")
	if !found || cluster == "" || service == "" {
		return "", "", fmt.Errorf("ecs ref must be '<cluster>/<service>': %q", ref)
	}
	return cluster, service, nil
}

// model.EcsDesiredCountTagKey を読み、未設定の場合は 1 とする
func desiredCountFromTags(tags map[string]string) (int32, error) {
	n, ok, err := tagInt32(tags, model.EcsDesiredCountTagKey)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 1, nil
	}
	if n <= 0 {
		return 0, fmt.Errorf("tag %s=%d must be positive", model.EcsDesiredCountTagKey, n)
	}
	return n, nil
}

// model.EcsScalingMinTagKey と EcsScalingMaxTagKey を読み、未設定の場合はそれぞれ独立に desiredCount を既定値とする
func scalingBoundsFromTags(tags map[string]string, desiredCount int32) (minimum, maximum int32, err error) {
	minimum = desiredCount
	if n, ok, err := tagInt32(tags, model.EcsScalingMinTagKey); err != nil {
		return 0, 0, err
	} else if ok {
		minimum = n
	}
	maximum = desiredCount
	if n, ok, err := tagInt32(tags, model.EcsScalingMaxTagKey); err != nil {
		return 0, 0, err
	} else if ok {
		maximum = n
	}
	// 3 つのタグは min <= desired-count <= max を満たさなければならない
	// desiredCount が上下限の外にある場合、UpdateService による変更の直後に Auto Scaling が上下限まで引き戻すため、指定した台数は実現しないまま、Start は成功として記録される
	// min > max もこの不等式が成立しない場合の 1 つであるため、検査はこの 1 つで足りる
	// 不正なタグは 1 つの値だけでは特定できない (min の既定値は desired-count である) ため、3 つの値をまとめて示す
	if desiredCount < minimum || desiredCount > maximum {
		return 0, 0, fmt.Errorf("tags must satisfy %s <= %s <= %s, got %d <= %d <= %d",
			model.EcsScalingMinTagKey, model.EcsDesiredCountTagKey, model.EcsScalingMaxTagKey,
			minimum, desiredCount, maximum)
	}
	return minimum, maximum, nil
}

// tags[key] を非負の int32 として解釈する
// タグが存在しない場合、および空の場合は ok が false となる
// この場合は不正な値と区別する。不正な値は既定値へ倒さず、エラーとする
func tagInt32(tags map[string]string, key string) (n int32, ok bool, err error) {
	v, present := tags[key]
	if !present || v == "" {
		return 0, false, nil
	}
	parsed, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return 0, false, fmt.Errorf("tag %s=%q is not an integer", key, v)
	}
	if parsed < 0 {
		return 0, false, fmt.Errorf("tag %s=%q must not be negative", key, v)
	}
	return int32(parsed), true, nil
}

func (t *EcsServiceTarget) scalableTarget(ctx context.Context, cluster, service string) (*aastypes.ScalableTarget, error) {
	out, err := t.AutoScaling.DescribeScalableTargets(ctx, &aas.DescribeScalableTargetsInput{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceIds:       []string{"service/" + cluster + "/" + service},
		ScalableDimension: scalableDimension,
	})
	if err != nil {
		return nil, err
	}
	if len(out.ScalableTargets) == 0 {
		return nil, nil
	}
	return &out.ScalableTargets[0], nil
}

func (t *EcsServiceTarget) register(ctx context.Context, cluster, service string, minimum, maximum int32) error {
	resourceID := "service/" + cluster + "/" + service
	_, err := t.AutoScaling.RegisterScalableTarget(ctx, &aas.RegisterScalableTargetInput{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceId:        &resourceID,
		ScalableDimension: scalableDimension,
		MinCapacity:       &minimum,
		MaxCapacity:       &maximum,
	})
	return err
}
