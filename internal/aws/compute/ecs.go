package compute

import (
	"context"
	"fmt"
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

// ECS サービスの起動規模とスケーリング上下限の管理方法をリソースタグから制御する。
// 停止時は scalable target の上下限を 0/0 とし、Scheduled Scaling を一時停止する。
// tags モードは通常のタグ範囲を維持し、scheduled モードは起動後の上下限変更を維持する。
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
	current, err := t.service(ctx, cluster, service)
	if err != nil {
		return model.Observation{}, err
	}
	if current == nil {
		return model.Observation{State: model.StateNotFound}, nil
	}
	config, err := ecsConfigFromTags(res.Tags)
	if err != nil {
		return model.Observation{}, err
	}
	scalable, err := t.scalableTarget(ctx, cluster, service)
	if err != nil {
		return model.Observation{}, err
	}
	observation := model.Observation{
		State:  ecsServiceState(current.DesiredCount, current.RunningCount, current.PendingCount),
		Detail: fmt.Sprintf("desiredCount=%d runningCount=%d pendingCount=%d", current.DesiredCount, current.RunningCount, current.PendingCount),
	}
	if scalable != nil {
		paused := ecsScheduledScalingPaused(scalable)
		maintainBounds := config.boundsMode == ecsBoundsModeTags
		observation.Detail += fmt.Sprintf(" minCapacity=%d maxCapacity=%d ScheduledScalingSuspended=%t", aws.ToInt32(scalable.MinCapacity), aws.ToInt32(scalable.MaxCapacity), paused)
		observation.NeedsStart = ecsNeedsInitialization(current, scalable) || paused != maintainBounds || (maintainBounds && !ecsBoundsEqual(scalable, config.minimum, config.maximum))
		observation.NeedsStop = !ecsBoundsEqual(scalable, 0, 0) || !paused
	}
	return observation, nil
}

// ECS サービスの停止完了だけを stopped として判定する。
// desiredCount とタスク数の差異は、停止操作を妨げない。
func ecsServiceState(desired, running, pending int32) model.ObservedState {
	if desired == 0 && running == 0 && pending == 0 {
		return model.StateStopped
	}
	return model.StateRunning
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
		return t.register(ctx, cluster, service, aws.Int32(0), aws.Int32(0), true)
	}
	var zero int32
	_, err = t.Ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: &cluster, Service: &service, DesiredCount: &zero})
	return err
}

// 起動規模の検証後、初期設定または稼働中の上下限管理モードを適用する。
func (t *EcsServiceTarget) Start(ctx context.Context, res model.Resource) error {
	cluster, service, err := splitEcsRef(res.Ref)
	if err != nil {
		return err
	}
	config, err := ecsConfigForStart(res)
	if err != nil {
		return err
	}
	current, err := t.service(ctx, cluster, service)
	if err != nil {
		return err
	}
	if current == nil {
		return fmt.Errorf("ecs service %s was not found", res.Ref)
	}
	scalable, err := t.scalableTarget(ctx, cluster, service)
	if err != nil {
		return err
	}
	maintainBounds := config.boundsMode == ecsBoundsModeTags
	if !ecsNeedsInitialization(current, scalable) {
		if scalable == nil {
			return nil
		}
		if maintainBounds && (!ecsScheduledScalingPaused(scalable) || !ecsBoundsEqual(scalable, config.minimum, config.maximum)) {
			return t.register(ctx, cluster, service, &config.minimum, &config.maximum, true)
		}
		if !maintainBounds && ecsScheduledScalingPaused(scalable) {
			return t.register(ctx, cluster, service, &config.minimum, &config.maximum, false)
		}
		return nil
	}
	if scalable != nil {
		// 起動設定の完了までは Scheduled Scaling を一時停止し、上下限を起動台数に固定する。
		// RegisterScalableTarget は範囲外の capacity を調整する: "adjusts the capacity of the scalable target to place it within these bounds"
		// https://docs.aws.amazon.com/autoscaling/application/APIReference/API_RegisterScalableTarget.html
		if !ecsBoundsEqual(scalable, config.desired, config.desired) || !ecsScheduledScalingPaused(scalable) {
			if err := t.register(ctx, cluster, service, &config.desired, &config.desired, true); err != nil {
				return err
			}
		}
		current, err = t.service(ctx, cluster, service)
		if err != nil {
			return err
		}
		if current == nil {
			return fmt.Errorf("ecs service %s was not found", res.Ref)
		}
	}
	if current.DesiredCount != config.desired {
		if _, err := t.Ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: &cluster, Service: &service, DesiredCount: &config.desired}); err != nil {
			return err
		}
	}
	if scalable != nil && (config.minimum != config.desired || config.maximum != config.desired || !maintainBounds) {
		return t.register(ctx, cluster, service, &config.minimum, &config.maximum, maintainBounds)
	}
	return nil
}

func (t *EcsServiceTarget) service(ctx context.Context, cluster, service string) (*ecstypes.Service, error) {
	out, err := t.Ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: &cluster, Services: []string{service}})
	if err != nil {
		return nil, err
	}
	for _, s := range out.Services {
		if s.Status != nil && *s.Status == "ACTIVE" {
			if s.SchedulingStrategy != "" && s.SchedulingStrategy != ecstypes.SchedulingStrategyReplica {
				return nil, fmt.Errorf("ecs service %s/%s uses unsupported scheduling strategy %q", cluster, service, s.SchedulingStrategy)
			}
			return &s, nil
		}
	}
	return nil, nil
}

func ecsScheduledScalingPaused(scalable *aastypes.ScalableTarget) bool {
	return scalable.SuspendedState != nil && aws.ToBool(scalable.SuspendedState.ScheduledScalingSuspended)
}

func ecsBoundsEqual(scalable *aastypes.ScalableTarget, minimum, maximum int32) bool {
	return aws.ToInt32(scalable.MinCapacity) == minimum && aws.ToInt32(scalable.MaxCapacity) == maximum
}

// 停止状態または上下限 0/0 を、起動台数の初期設定対象とする。
func ecsNeedsInitialization(service *ecstypes.Service, scalable *aastypes.ScalableTarget) bool {
	return ecsServiceState(service.DesiredCount, service.RunningCount, service.PendingCount) == model.StateStopped ||
		(scalable != nil && ecsBoundsEqual(scalable, 0, 0))
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

func (t *EcsServiceTarget) register(ctx context.Context, cluster, service string, minimum, maximum *int32, paused bool) error {
	resourceID := "service/" + cluster + "/" + service
	_, err := t.AutoScaling.RegisterScalableTarget(ctx, &aas.RegisterScalableTargetInput{
		ServiceNamespace:  aastypes.ServiceNamespaceEcs,
		ResourceId:        &resourceID,
		ScalableDimension: scalableDimension,
		MinCapacity:       minimum,
		MaxCapacity:       maximum,
		SuspendedState:    &aastypes.SuspendedState{ScheduledScalingSuspended: aws.Bool(paused)},
	})
	return err
}
