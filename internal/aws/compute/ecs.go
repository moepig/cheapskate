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
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

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

func (t *EcsServiceTarget) Describe(ctx context.Context, res model.Resource) (model.Observation, error) {
	cluster, service, err := splitEcsRef(res.Ref)
	if err != nil {
		return model.Observation{}, err
	}
	out, err := t.Ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: &cluster, Services: []string{service}})
	if err != nil {
		return model.Observation{}, err
	}
	for _, s := range out.Services {
		if s.Status != nil && *s.Status == "ACTIVE" {
			if s.SchedulingStrategy != ecstypes.SchedulingStrategyReplica {
				return model.Observation{}, fmt.Errorf("ecs service %s uses unsupported scheduling strategy %q", res.Ref, s.SchedulingStrategy)
			}
			observation := model.Observation{
				State:  ecsServiceState(s.DesiredCount, s.RunningCount, s.PendingCount),
				Detail: fmt.Sprintf("desiredCount=%d runningCount=%d pendingCount=%d", s.DesiredCount, s.RunningCount, s.PendingCount),
			}
			config, err := ecsConfigFromTags(res.Tags)
			if err != nil {
				return model.Observation{}, err
			}
			scalable, err := t.scalableTarget(ctx, cluster, service)
			if err != nil {
				return model.Observation{}, err
			}
			if observation.State == model.StateRunning && scalable != nil && (aws.ToInt32(scalable.MinCapacity) != config.minimum || aws.ToInt32(scalable.MaxCapacity) != config.maximum) {
				observation.NeedsStart = true
			}
			return observation, nil
		}
	}
	return model.Observation{State: model.StateNotFound}, nil
}

// ECS サービスの稼働状態をタスク数だけから判定する。
// desiredCount の変更がタスクへ反映される途中では、追加の start/stop を送らない。
func ecsServiceState(desired, running, pending int32) model.ObservedState {
	if desired == 0 && running == 0 && pending == 0 {
		return model.StateStopped
	}
	if desired > 0 && running == desired && pending == 0 {
		return model.StateRunning
	}
	return model.StateTransitioning
}

func (t *EcsServiceTarget) Stop(ctx context.Context, res model.Resource) error {
	if _, err := ecsConfigFromTags(res.Tags); err != nil {
		return err
	}
	cluster, service, err := splitEcsRef(res.Ref)
	if err != nil {
		return err
	}
	scalable, err := t.scalableTarget(ctx, cluster, service)
	if err != nil {
		return err
	}
	if scalable != nil {
		return t.register(ctx, cluster, service, 0, 0)
	}
	var zero int32
	_, err = t.Ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: &cluster, Service: &service, DesiredCount: &zero})
	return err
}

// Auto Scaling の min/max をタグ値へ復元し、必要な場合に desiredCount を復元する。
func (t *EcsServiceTarget) Start(ctx context.Context, res model.Resource) error {
	cluster, service, err := splitEcsRef(res.Ref)
	if err != nil {
		return err
	}
	config, err := ecsConfigFromTags(res.Tags)
	if err != nil {
		return err
	}
	scalable, err := t.scalableTarget(ctx, cluster, service)
	if err != nil {
		return err
	}
	if scalable != nil {
		if aws.ToInt32(scalable.MinCapacity) != config.minimum || aws.ToInt32(scalable.MaxCapacity) != config.maximum {
			if err := t.register(ctx, cluster, service, config.minimum, config.maximum); err != nil {
				return err
			}
		}
	}
	currentDesired, err := t.desiredCount(ctx, cluster, service)
	if err != nil {
		return err
	}
	if currentDesired == config.desired {
		return nil
	}
	_, err = t.Ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: &cluster, Service: &service, DesiredCount: &config.desired})
	return err
}

func (t *EcsServiceTarget) desiredCount(ctx context.Context, cluster, service string) (int32, error) {
	out, err := t.Ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: &cluster, Services: []string{service}})
	if err != nil {
		return 0, err
	}
	for _, s := range out.Services {
		if s.Status != nil && *s.Status == "ACTIVE" {
			if s.SchedulingStrategy != ecstypes.SchedulingStrategyReplica {
				return 0, fmt.Errorf("ecs service %s/%s uses unsupported scheduling strategy %q", cluster, service, s.SchedulingStrategy)
			}
			return s.DesiredCount, nil
		}
	}
	return 0, fmt.Errorf("ecs service %s/%s was not found", cluster, service)
}

type ecsConfig struct {
	desired int32
	minimum int32
	maximum int32
}

func ecsConfigFromTags(tags map[string]string) (ecsConfig, error) {
	desired, err := desiredCountFromTags(tags)
	if err != nil {
		return ecsConfig{}, err
	}
	minimum, maximum, err := scalingBoundsFromTags(tags, desired)
	if err != nil {
		return ecsConfig{}, err
	}
	return ecsConfig{desired: desired, minimum: minimum, maximum: maximum}, nil
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
	if !present {
		return 0, false, nil
	}
	if v == "" {
		return 0, false, fmt.Errorf("tag %s must not be empty", key)
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
