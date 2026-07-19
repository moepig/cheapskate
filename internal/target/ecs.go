package target

import (
	"context"
	"fmt"
	"strings"

	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"cheapskate/internal/model"
)

const scalableDimension = aastypes.ScalableDimensionECSServiceDesiredCount

// EcsAPI is the subset of the ECS client the target uses.
type EcsAPI interface {
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	UpdateService(ctx context.Context, in *ecs.UpdateServiceInput, opts ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error)
}

// AutoScalingAPI is the subset of the Application Auto Scaling client used.
type AutoScalingAPI interface {
	DescribeScalableTargets(ctx context.Context, in *aas.DescribeScalableTargetsInput, opts ...func(*aas.Options)) (*aas.DescribeScalableTargetsOutput, error)
	RegisterScalableTarget(ctx context.Context, in *aas.RegisterScalableTargetInput, opts ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error)
}

// EcsServiceTarget: stop = desiredCount 0, start = restore the count.
//
// When the service has an Application Auto Scaling target, its min/max are set to 0/0 on stop (a scaling policy would otherwise undo the desiredCount change) and restored from the status item on start.
type EcsServiceTarget struct {
	Ecs         EcsAPI
	AutoScaling AutoScalingAPI
}

func (t *EcsServiceTarget) Type() string { return model.TypeEcsService }

func (t *EcsServiceTarget) Describe(ctx context.Context, ref string) (model.Observation, error) {
	cluster, service, err := parseEcsRef(ref)
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
			desired := s.DesiredCount
			return model.Observation{
				State:        state,
				Detail:       fmt.Sprintf("desiredCount=%d", desired),
				DesiredCount: &desired,
			}, nil
		}
	}
	return model.Observation{State: model.StateNotFound}, nil
}

// PrepareStop is read-only: it determines what must be restorable on Start and returns it for the caller to persist before Stop mutates anything.
//
// A desiredCount or scaling min/max of 0 at this point means cheapskate itself already stopped the service (or someone else pinned it to 0/0); that field is left nil so the caller's write does not clobber a real saved value with a zero (B-2).
func (t *EcsServiceTarget) PrepareStop(ctx context.Context, ref string, _ model.Config, _ model.Status) (*model.SavedState, error) {
	cluster, service, err := parseEcsRef(ref)
	if err != nil {
		return nil, err
	}
	obs, err := t.Describe(ctx, ref)
	if err != nil {
		return nil, err
	}
	saved := &model.SavedState{}
	if obs.DesiredCount != nil && *obs.DesiredCount > 0 {
		saved.DesiredCount = obs.DesiredCount
	}
	scalable, err := t.scalableTarget(ctx, cluster, service)
	if err != nil {
		return nil, err
	}
	if scalable != nil && (*scalable.MinCapacity != 0 || *scalable.MaxCapacity != 0) {
		saved.ScalingMin = scalable.MinCapacity
		saved.ScalingMax = scalable.MaxCapacity
	}
	return saved, nil
}

func (t *EcsServiceTarget) Stop(ctx context.Context, ref string, _ model.Config, _ model.Status) error {
	cluster, service, err := parseEcsRef(ref)
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
	_, err = t.Ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: &cluster, Service: &service, DesiredCount: &zero})
	return err
}

func (t *EcsServiceTarget) Start(ctx context.Context, ref string, cfg model.Config, status model.Status) (*model.SavedState, error) {
	cluster, service, err := parseEcsRef(ref)
	if err != nil {
		return nil, err
	}
	count := restoreCount(cfg, status)
	if status.SavedScalingMin != nil {
		max := *status.SavedScalingMin
		if status.SavedScalingMax != nil {
			max = *status.SavedScalingMax
		}
		if err := t.register(ctx, cluster, service, *status.SavedScalingMin, max); err != nil {
			return nil, err
		}
	}
	_, err = t.Ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: &cluster, Service: &service, DesiredCount: &count})
	return nil, err
}

// restoreCount: config.restore_count > count saved at stop time > 1.
func restoreCount(cfg model.Config, status model.Status) int32 {
	if cfg.RestoreCount != nil && *cfg.RestoreCount > 0 {
		return *cfg.RestoreCount
	}
	if status.SavedDesiredCount != nil && *status.SavedDesiredCount > 0 {
		return *status.SavedDesiredCount
	}
	return 1
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

func parseEcsRef(ref string) (cluster, service string, err error) {
	cluster, service, found := strings.Cut(ref, "/")
	if !found || cluster == "" || service == "" {
		return "", "", fmt.Errorf("ecs ref must be '<cluster>/<service>': %q", ref)
	}
	return cluster, service, nil
}
