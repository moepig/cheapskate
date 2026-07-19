package target

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"

	"cheapskate/internal/model"
)

// RdsAPI is the subset of the RDS client the targets use.
type RdsAPI interface {
	DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, opts ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	DescribeDBClusters(ctx context.Context, in *rds.DescribeDBClustersInput, opts ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
	StopDBInstance(ctx context.Context, in *rds.StopDBInstanceInput, opts ...func(*rds.Options)) (*rds.StopDBInstanceOutput, error)
	StartDBInstance(ctx context.Context, in *rds.StartDBInstanceInput, opts ...func(*rds.Options)) (*rds.StartDBInstanceOutput, error)
	StopDBCluster(ctx context.Context, in *rds.StopDBClusterInput, opts ...func(*rds.Options)) (*rds.StopDBClusterOutput, error)
	StartDBCluster(ctx context.Context, in *rds.StartDBClusterInput, opts ...func(*rds.Options)) (*rds.StartDBClusterOutput, error)
}

// RdsInstanceTarget manages RDS DB instances.
type RdsInstanceTarget struct {
	Client RdsAPI
}

func (t *RdsInstanceTarget) Type() string { return model.TypeRdsInstance }

func (t *RdsInstanceTarget) Describe(ctx context.Context, ref string) (model.Observation, error) {
	out, err := t.Client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: &ref})
	if err != nil {
		var nf *types.DBInstanceNotFoundFault
		if errors.As(err, &nf) {
			return model.Observation{State: model.StateNotFound}, nil
		}
		return model.Observation{}, err
	}
	if len(out.DBInstances) == 0 {
		return model.Observation{State: model.StateNotFound}, nil
	}
	return rdsObservation(*out.DBInstances[0].DBInstanceStatus), nil
}

func (t *RdsInstanceTarget) PrepareStop(_ context.Context, _ string, _ model.Member, _ model.Status) (*model.SavedState, error) {
	return nil, nil
}

func (t *RdsInstanceTarget) Stop(ctx context.Context, ref string, _ model.Member, _ model.Status) error {
	_, err := t.Client.StopDBInstance(ctx, &rds.StopDBInstanceInput{DBInstanceIdentifier: &ref})
	return err
}

func (t *RdsInstanceTarget) Start(ctx context.Context, ref string, _ model.Member, _ model.Status) (*model.SavedState, error) {
	_, err := t.Client.StartDBInstance(ctx, &rds.StartDBInstanceInput{DBInstanceIdentifier: &ref})
	return nil, err
}

// RdsClusterTarget manages Aurora clusters.
type RdsClusterTarget struct {
	Client RdsAPI
}

func (t *RdsClusterTarget) Type() string { return model.TypeRdsCluster }

func (t *RdsClusterTarget) Describe(ctx context.Context, ref string) (model.Observation, error) {
	out, err := t.Client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: &ref})
	if err != nil {
		var nf *types.DBClusterNotFoundFault
		if errors.As(err, &nf) {
			return model.Observation{State: model.StateNotFound}, nil
		}
		return model.Observation{}, err
	}
	if len(out.DBClusters) == 0 {
		return model.Observation{State: model.StateNotFound}, nil
	}
	return rdsObservation(*out.DBClusters[0].Status), nil
}

func (t *RdsClusterTarget) PrepareStop(_ context.Context, _ string, _ model.Member, _ model.Status) (*model.SavedState, error) {
	return nil, nil
}

func (t *RdsClusterTarget) Stop(ctx context.Context, ref string, _ model.Member, _ model.Status) error {
	_, err := t.Client.StopDBCluster(ctx, &rds.StopDBClusterInput{DBClusterIdentifier: &ref})
	return err
}

func (t *RdsClusterTarget) Start(ctx context.Context, ref string, _ model.Member, _ model.Status) (*model.SavedState, error) {
	_, err := t.Client.StartDBCluster(ctx, &rds.StartDBClusterInput{DBClusterIdentifier: &ref})
	return nil, err
}

// Anything that is neither available nor stopped is "transitioning"; the reconciler skips it and retries next cycle instead of waiting.
func rdsObservation(rawStatus string) model.Observation {
	switch rawStatus {
	case "available":
		return model.Observation{State: model.StateRunning, Detail: rawStatus}
	case "stopped":
		return model.Observation{State: model.StateStopped, Detail: rawStatus}
	default:
		return model.Observation{State: model.StateTransitioning, Detail: rawStatus}
	}
}
