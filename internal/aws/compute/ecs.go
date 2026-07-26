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

// stop は desiredCount を 0 にし、start はリソース自身の model.EcsDesiredCountTagKey タグから desiredCount を取る（未設定なら既定の 1）
//
// サービスに Application Auto Scaling のターゲットがある場合、stop 時にはその min/max を 0/0 にする
// そうしないとスケーリングポリシーが desiredCount の変更を巻き戻してしまう
// start 時には model.EcsScalingMinTagKey と EcsScalingMaxTagKey から書き戻す（未設定なら desired count 自体を既定値とする）
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

// stop は 2 段階（スケーラブルターゲットを 0/0 → desiredCount を 0）で、原子的ではない
// 後段だけが失敗すると、サービスは起動したまま Auto Scaling だけが 0/0 に固定されて残る
// スケールアウトできない状態であり、しかも「停止に失敗した」というエラーからはそうなっていると読み取れない
// そのため後段が失敗したら前段を巻き戻す
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
			return err // 0/0 にしたものがないので巻き戻すものもない
		}
		return t.rollbackFailedStop(ctx, cluster, service, scalable, err)
	}
	return nil
}

// 0/0 に潰したスケーラブルターゲットを、DescribeScalableTargets が返していた元の min/max へ戻す
// 元の値はすでに手元にある（scalableTarget が ScalableTarget をそのまま返す）ので、追加の API 呼び出しも IAM 権限も要らない
// この関数は必ずエラーを返す
// 巻き戻せたかどうかにかかわらず停止自体は失敗しており、その事実は呼び出し側で握りつぶされてはならないためである
// 巻き戻しにも失敗した場合は、0/0 のまま人手で直す必要があることをエラー本文に明示する
// これが status# の last_error と SNS 通知にそのまま載る
func (t *EcsServiceTarget) rollbackFailedStop(ctx context.Context, cluster, service string, prev *aastypes.ScalableTarget, cause error) error {
	minimum, maximum := aws.ToInt32(prev.MinCapacity), aws.ToInt32(prev.MaxCapacity)
	if rerr := t.register(ctx, cluster, service, minimum, maximum); rerr != nil {
		return fmt.Errorf("stop failed: %w; scalable target is left clamped at 0/0 and must be restored to %d/%d by hand (rollback failed: %v)",
			cause, minimum, maximum, rerr)
	}
	return fmt.Errorf("stop failed: %w; scalable target rolled back to %d/%d", cause, minimum, maximum)
}

// start も 2 段階だが、Stop と違って巻き戻さない
// 前段で min が 1 以上に戻った時点で Auto Scaling 自身がサービスを立ち上げにいくので、後段の UpdateService が失敗しても残る状態は「起動しようとしている」であり、望んだ向きと同じである
// 次サイクルの再試行が desiredCount を正確な値に揃える
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

// ecs-service の Ref を、ECS API が別々の引数として要求する cluster と service へ分解する
// この分解が要るのは ECS API を呼ぶ側だけなので、Ref の文法を宣言しているドメイン（model の ecs-service の RefPattern）ではなくここに置く
//
// 探索を通ったリソースの Ref はすでにその文法で検証済みである（model.Resource.Validate）
// それでもここで確かめるのは、cluster か service が空のまま DescribeServices や UpdateService を呼ばないための歯止めである
// 空文字のクラスタ名は ECS 側では「default クラスタ」を意味するので、黙って別のクラスタへ操作が飛びうる
func splitEcsRef(ref string) (cluster, service string, err error) {
	cluster, service, found := strings.Cut(ref, "/")
	if !found || cluster == "" || service == "" {
		return "", "", fmt.Errorf("ecs ref must be '<cluster>/<service>': %q", ref)
	}
	return cluster, service, nil
}

// model.EcsDesiredCountTagKey を読み、未設定なら既定の 1 とする
// そのタグを持たないリソースを初めて start する場合などがこれにあたる
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

// model.EcsScalingMinTagKey と EcsScalingMaxTagKey を読み、それぞれ未設定なら独立に desiredCount を既定値とする
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
	// desiredCount が上下限の外にあると、Start は「UpdateService でその台数にした直後に Auto Scaling が上下限まで引き戻す」を必ず引き起こし、オペレータの指定した台数が黙って実現しないまま成功として記録される
	// min > max もこの不等式が破れる場合の 1 つなので、検査はこれ 1 つで足りる
	// どのタグが悪いのかは一方だけでは決まらない（min の既定値は desired-count である）ため、3 つの値をまとめて示す
	if desiredCount < minimum || desiredCount > maximum {
		return 0, 0, fmt.Errorf("tags must satisfy %s <= %s <= %s, got %d <= %d <= %d",
			model.EcsScalingMinTagKey, model.EcsDesiredCountTagKey, model.EcsScalingMaxTagKey,
			minimum, desiredCount, maximum)
	}
	return minimum, maximum, nil
}

// tags[key] を非負の int32 として解釈する
// タグがない、または空のときは ok が false になる
// これは不正な値とは区別され、不正な値は黙って既定値にせずエラーにする
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
