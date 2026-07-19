package target

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"

	"cheapskate/internal/model"
)

var errOther = errors.New("some other AWS error")

type fakeRds struct {
	instances   []types.DBInstance
	clusters    []types.DBCluster
	describeErr error
	stopped     []string
	started     []string
}

func (f *fakeRds) DescribeDBInstances(_ context.Context, _ *rds.DescribeDBInstancesInput, _ ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	return &rds.DescribeDBInstancesOutput{DBInstances: f.instances}, nil
}

func (f *fakeRds) DescribeDBClusters(_ context.Context, _ *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	return &rds.DescribeDBClustersOutput{DBClusters: f.clusters}, nil
}

func (f *fakeRds) StopDBInstance(_ context.Context, in *rds.StopDBInstanceInput, _ ...func(*rds.Options)) (*rds.StopDBInstanceOutput, error) {
	f.stopped = append(f.stopped, *in.DBInstanceIdentifier)
	return &rds.StopDBInstanceOutput{}, nil
}

func (f *fakeRds) StartDBInstance(_ context.Context, in *rds.StartDBInstanceInput, _ ...func(*rds.Options)) (*rds.StartDBInstanceOutput, error) {
	f.started = append(f.started, *in.DBInstanceIdentifier)
	return &rds.StartDBInstanceOutput{}, nil
}

func (f *fakeRds) StopDBCluster(_ context.Context, in *rds.StopDBClusterInput, _ ...func(*rds.Options)) (*rds.StopDBClusterOutput, error) {
	f.stopped = append(f.stopped, *in.DBClusterIdentifier)
	return &rds.StopDBClusterOutput{}, nil
}

func (f *fakeRds) StartDBCluster(_ context.Context, in *rds.StartDBClusterInput, _ ...func(*rds.Options)) (*rds.StartDBClusterOutput, error) {
	f.started = append(f.started, *in.DBClusterIdentifier)
	return &rds.StartDBClusterOutput{}, nil
}

// rdsObservation's status-string -> Observation mapping is the exact behavior DESIGN.md specifies: anything but "available"/"stopped" is transitioning, so the reconciler retries next cycle instead of waiting.
func TestRdsObservationStateMapping(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"available", model.StateRunning},
		{"stopped", model.StateStopped},
		{"stopping", model.StateTransitioning},
		{"starting", model.StateTransitioning},
		{"backing-up", model.StateTransitioning},
		{"", model.StateTransitioning},
	}
	for _, tc := range cases {
		obs := rdsObservation(tc.raw)
		if obs.State != tc.want {
			t.Errorf("rdsObservation(%q).State = %q, want %q", tc.raw, obs.State, tc.want)
		}
		if obs.Detail != tc.raw {
			t.Errorf("rdsObservation(%q).Detail = %q, want %q", tc.raw, obs.Detail, tc.raw)
		}
	}
}

func TestRdsInstanceDescribeNotFoundFault(t *testing.T) {
	f := &fakeRds{describeErr: &types.DBInstanceNotFoundFault{}}
	tgt := &RdsInstanceTarget{Client: f}

	obs, err := tgt.Describe(context.Background(), "gone")
	if err != nil {
		t.Fatalf("NotFoundFault must convert to StateNotFound, not an error: %v", err)
	}
	if obs.State != model.StateNotFound {
		t.Fatalf("state: %q", obs.State)
	}
}

// DESIGN.md: Describe returning an empty list (rather than an error) must also be treated as not-found.
func TestRdsInstanceDescribeEmptyListIsNotFound(t *testing.T) {
	f := &fakeRds{instances: nil}
	tgt := &RdsInstanceTarget{Client: f}

	obs, err := tgt.Describe(context.Background(), "gone")
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != model.StateNotFound {
		t.Fatalf("state: %q", obs.State)
	}
}

func TestRdsInstanceDescribeOtherErrorPassesThrough(t *testing.T) {
	f := &fakeRds{describeErr: errOther}
	tgt := &RdsInstanceTarget{Client: f}

	_, err := tgt.Describe(context.Background(), "db")
	if !errors.Is(err, errOther) {
		t.Fatalf("non-NotFound error must pass through unchanged, got %v", err)
	}
}

func TestRdsInstanceDescribeRunning(t *testing.T) {
	status := "available"
	f := &fakeRds{instances: []types.DBInstance{{DBInstanceStatus: &status}}}
	tgt := &RdsInstanceTarget{Client: f}

	obs, err := tgt.Describe(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != model.StateRunning {
		t.Fatalf("state: %q", obs.State)
	}
}

func TestRdsInstanceStopStart(t *testing.T) {
	f := &fakeRds{}
	tgt := &RdsInstanceTarget{Client: f}

	if err := tgt.Stop(context.Background(), "db", model.Config{}, model.Status{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tgt.Start(context.Background(), "db", model.Config{}, model.Status{}); err != nil {
		t.Fatal(err)
	}
	if len(f.stopped) != 1 || f.stopped[0] != "db" {
		t.Fatalf("stopped: %v", f.stopped)
	}
	if len(f.started) != 1 || f.started[0] != "db" {
		t.Fatalf("started: %v", f.started)
	}
	if saved, err := tgt.PrepareStop(context.Background(), "db", model.Config{}, model.Status{}); err != nil || saved != nil {
		t.Fatalf("RDS has nothing to save before stop: %+v, %v", saved, err)
	}
}

func TestRdsClusterDescribeNotFoundFault(t *testing.T) {
	f := &fakeRds{describeErr: &types.DBClusterNotFoundFault{}}
	tgt := &RdsClusterTarget{Client: f}

	obs, err := tgt.Describe(context.Background(), "gone")
	if err != nil {
		t.Fatalf("NotFoundFault must convert to StateNotFound, not an error: %v", err)
	}
	if obs.State != model.StateNotFound {
		t.Fatalf("state: %q", obs.State)
	}
}

func TestRdsClusterDescribeEmptyListIsNotFound(t *testing.T) {
	f := &fakeRds{clusters: nil}
	tgt := &RdsClusterTarget{Client: f}

	obs, err := tgt.Describe(context.Background(), "gone")
	if err != nil {
		t.Fatal(err)
	}
	if obs.State != model.StateNotFound {
		t.Fatalf("state: %q", obs.State)
	}
}

func TestRdsClusterDescribeOtherErrorPassesThrough(t *testing.T) {
	f := &fakeRds{describeErr: errOther}
	tgt := &RdsClusterTarget{Client: f}

	_, err := tgt.Describe(context.Background(), "cluster")
	if !errors.Is(err, errOther) {
		t.Fatalf("non-NotFound error must pass through unchanged, got %v", err)
	}
}

func TestRdsClusterStopStart(t *testing.T) {
	f := &fakeRds{}
	tgt := &RdsClusterTarget{Client: f}

	if err := tgt.Stop(context.Background(), "cluster", model.Config{}, model.Status{}); err != nil {
		t.Fatal(err)
	}
	if _, err := tgt.Start(context.Background(), "cluster", model.Config{}, model.Status{}); err != nil {
		t.Fatal(err)
	}
	if len(f.stopped) != 1 || f.stopped[0] != "cluster" {
		t.Fatalf("stopped: %v", f.stopped)
	}
	if len(f.started) != 1 || f.started[0] != "cluster" {
		t.Fatalf("started: %v", f.started)
	}
}
