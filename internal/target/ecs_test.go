package target

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	aastypes "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"cheapskate/internal/model"
)

type fakeEcs struct {
	desiredCount int32
	updates      []int32
}

func (f *fakeEcs) DescribeServices(_ context.Context, _ *ecs.DescribeServicesInput, _ ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	return &ecs.DescribeServicesOutput{Services: []ecstypes.Service{{
		Status:       aws.String("ACTIVE"),
		DesiredCount: f.desiredCount,
	}}}, nil
}

func (f *fakeEcs) UpdateService(_ context.Context, in *ecs.UpdateServiceInput, _ ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	f.updates = append(f.updates, *in.DesiredCount)
	f.desiredCount = *in.DesiredCount
	return &ecs.UpdateServiceOutput{}, nil
}

type registration struct{ min, max int32 }

type fakeAas struct {
	target        *aastypes.ScalableTarget
	registrations []registration
}

func (f *fakeAas) DescribeScalableTargets(_ context.Context, _ *aas.DescribeScalableTargetsInput, _ ...func(*aas.Options)) (*aas.DescribeScalableTargetsOutput, error) {
	out := &aas.DescribeScalableTargetsOutput{}
	if f.target != nil {
		out.ScalableTargets = []aastypes.ScalableTarget{*f.target}
	}
	return out, nil
}

func (f *fakeAas) RegisterScalableTarget(_ context.Context, in *aas.RegisterScalableTargetInput, _ ...func(*aas.Options)) (*aas.RegisterScalableTargetOutput, error) {
	f.registrations = append(f.registrations, registration{*in.MinCapacity, *in.MaxCapacity})
	return &aas.RegisterScalableTargetOutput{}, nil
}

func i32(v int32) *int32 { return &v }

func TestEcsPrepareStopReturnsCountAndScaling(t *testing.T) {
	e := &fakeEcs{desiredCount: 3}
	a := &fakeAas{target: &aastypes.ScalableTarget{MinCapacity: i32(2), MaxCapacity: i32(6)}}
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	saved, err := tgt.PrepareStop(context.Background(), "dev/api", model.Config{}, model.Status{})
	if err != nil {
		t.Fatal(err)
	}
	if saved.DesiredCount == nil || *saved.DesiredCount != 3 {
		t.Fatalf("saved desired count: %v", saved.DesiredCount)
	}
	if saved.ScalingMin == nil || *saved.ScalingMin != 2 || saved.ScalingMax == nil || *saved.ScalingMax != 6 {
		t.Fatalf("saved scaling: %+v", saved)
	}
	if len(a.registrations) != 0 || len(e.updates) != 0 {
		t.Fatal("PrepareStop must not mutate anything")
	}
}

func TestEcsPrepareStopKeepsExistingSavedValuesWhenAlreadyZero(t *testing.T) {
	// desiredCount and scaling already at 0/0 means cheapskate (or someone) already stopped this; PrepareStop must not report a zero that would clobber the real saved value (B-2).
	e := &fakeEcs{desiredCount: 0}
	a := &fakeAas{target: &aastypes.ScalableTarget{MinCapacity: i32(0), MaxCapacity: i32(0)}}
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	saved, err := tgt.PrepareStop(context.Background(), "dev/api", model.Config{}, model.Status{})
	if err != nil {
		t.Fatal(err)
	}
	if saved.DesiredCount != nil {
		t.Fatalf("desired count must be left nil, got %v", *saved.DesiredCount)
	}
	if saved.ScalingMin != nil || saved.ScalingMax != nil {
		t.Fatalf("scaling must be left nil, got min=%v max=%v", saved.ScalingMin, saved.ScalingMax)
	}
}

func TestEcsStopPinsScalingAndZeroesDesiredCount(t *testing.T) {
	e := &fakeEcs{desiredCount: 3}
	a := &fakeAas{target: &aastypes.ScalableTarget{MinCapacity: i32(2), MaxCapacity: i32(6)}}
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	if err := tgt.Stop(context.Background(), "dev/api", model.Config{}, model.Status{}); err != nil {
		t.Fatal(err)
	}
	if len(a.registrations) != 1 || a.registrations[0] != (registration{0, 0}) {
		t.Fatalf("scaling must be pinned to 0/0: %v", a.registrations)
	}
	if len(e.updates) != 1 || e.updates[0] != 0 {
		t.Fatalf("desiredCount updates: %v", e.updates)
	}
}

func TestEcsStopWithoutScalingTarget(t *testing.T) {
	e := &fakeEcs{desiredCount: 2}
	a := &fakeAas{}
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}

	if err := tgt.Stop(context.Background(), "dev/api", model.Config{}, model.Status{}); err != nil {
		t.Fatal(err)
	}
	if len(a.registrations) != 0 {
		t.Fatal("no scaling target: must not register")
	}
}

func TestEcsStartRestoresScalingAndCount(t *testing.T) {
	e := &fakeEcs{desiredCount: 0}
	a := &fakeAas{}
	tgt := &EcsServiceTarget{Ecs: e, AutoScaling: a}
	status := model.Status{SavedDesiredCount: i32(3), SavedScalingMin: i32(2), SavedScalingMax: i32(6)}

	if _, err := tgt.Start(context.Background(), "dev/api", model.Config{}, status); err != nil {
		t.Fatal(err)
	}
	if len(a.registrations) != 1 || a.registrations[0] != (registration{2, 6}) {
		t.Fatalf("scaling restore: %v", a.registrations)
	}
	if len(e.updates) != 1 || e.updates[0] != 3 {
		t.Fatalf("desiredCount restore: %v", e.updates)
	}
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
		if got := restoreCount(tc.cfg, tc.status); got != tc.want {
			t.Errorf("case %d: want %d, got %d", i, tc.want, got)
		}
	}
}

func TestEcsDescribeStates(t *testing.T) {
	tgt := &EcsServiceTarget{Ecs: &fakeEcs{desiredCount: 2}, AutoScaling: &fakeAas{}}
	obs, err := tgt.Describe(context.Background(), "dev/api")
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != model.StateRunning || *obs.DesiredCount != 2 {
		t.Fatalf("obs: %+v", obs)
	}

	tgt = &EcsServiceTarget{Ecs: &fakeEcs{desiredCount: 0}, AutoScaling: &fakeAas{}}
	obs, err = tgt.Describe(context.Background(), "dev/api")
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != model.StateStopped {
		t.Fatalf("obs: %+v", obs)
	}

	if _, err := tgt.Describe(context.Background(), "noslash"); err == nil {
		t.Fatal("want error for malformed ecs ref")
	}
}
