package compute

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"

	"cheapskate/internal/core/model"
)

// ターゲットが使う RDS クライアントの部分集合
type RdsAPI interface {
	DescribeDBInstances(ctx context.Context, in *rds.DescribeDBInstancesInput, opts ...func(*rds.Options)) (*rds.DescribeDBInstancesOutput, error)
	DescribeDBClusters(ctx context.Context, in *rds.DescribeDBClustersInput, opts ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
	StopDBInstance(ctx context.Context, in *rds.StopDBInstanceInput, opts ...func(*rds.Options)) (*rds.StopDBInstanceOutput, error)
	StartDBInstance(ctx context.Context, in *rds.StartDBInstanceInput, opts ...func(*rds.Options)) (*rds.StartDBInstanceOutput, error)
	StopDBCluster(ctx context.Context, in *rds.StopDBClusterInput, opts ...func(*rds.Options)) (*rds.StopDBClusterOutput, error)
	StartDBCluster(ctx context.Context, in *rds.StartDBClusterInput, opts ...func(*rds.Options)) (*rds.StartDBClusterOutput, error)
}

// RDS の DB インスタンスを管理する
type RdsInstanceTarget struct {
	Client RdsAPI
}

func (t *RdsInstanceTarget) Type() model.ResourceType { return model.TypeRdsInstance }

// ステータスは RDS のレスポンスにおいてポインタであり、nil となりうる
// EC2 の State と同じく、状態を判定できないインスタンスを running や stopped として操作してはならないため、スキップする
// 加えて、nil を無条件に参照した場合は panic となる
// reconciler は Lambda 上で動作するため、この panic はリソース 1 件の失敗にとどまらず呼び出し全体を失敗させる
// 1 件の失敗を他のリソースへ波及させないという不変条件が成立しなくなる
func (t *RdsInstanceTarget) Describe(ctx context.Context, ref string) (model.Observation, error) {
	out, err := t.Client.DescribeDBInstances(ctx, &rds.DescribeDBInstancesInput{DBInstanceIdentifier: &ref})
	if err != nil {
		if _, ok := errors.AsType[*types.DBInstanceNotFoundFault](err); ok {
			return model.Observation{State: model.StateNotFound}, nil
		}
		return model.Observation{}, err
	}
	for _, inst := range out.DBInstances {
		if inst.DBInstanceStatus == nil {
			continue
		}
		return rdsObservation(*inst.DBInstanceStatus), nil
	}
	return model.Observation{State: model.StateNotFound}, nil
}

func (t *RdsInstanceTarget) Stop(ctx context.Context, ref string) error {
	_, err := t.Client.StopDBInstance(ctx, &rds.StopDBInstanceInput{DBInstanceIdentifier: &ref})
	return err
}

func (t *RdsInstanceTarget) Start(ctx context.Context, res model.Resource) error {
	_, err := t.Client.StartDBInstance(ctx, &rds.StartDBInstanceInput{DBInstanceIdentifier: &res.Ref})
	return err
}

// Aurora クラスタを管理する
type RdsClusterTarget struct {
	Client RdsAPI
}

func (t *RdsClusterTarget) Type() model.ResourceType { return model.TypeRdsCluster }

// ステータスが nil のクラスタをスキップする理由は、RdsInstanceTarget.Describe と同じである
func (t *RdsClusterTarget) Describe(ctx context.Context, ref string) (model.Observation, error) {
	out, err := t.Client.DescribeDBClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: &ref})
	if err != nil {
		if _, ok := errors.AsType[*types.DBClusterNotFoundFault](err); ok {
			return model.Observation{State: model.StateNotFound}, nil
		}
		return model.Observation{}, err
	}
	for _, c := range out.DBClusters {
		if c.Status == nil {
			continue
		}
		return rdsObservation(*c.Status), nil
	}
	return model.Observation{State: model.StateNotFound}, nil
}

func (t *RdsClusterTarget) Stop(ctx context.Context, ref string) error {
	_, err := t.Client.StopDBCluster(ctx, &rds.StopDBClusterInput{DBClusterIdentifier: &ref})
	return err
}

func (t *RdsClusterTarget) Start(ctx context.Context, res model.Resource) error {
	_, err := t.Client.StartDBCluster(ctx, &rds.StartDBClusterInput{DBClusterIdentifier: &res.Ref})
	return err
}

// available と stopped のいずれでもない状態は、すべて "transitioning" とする
// reconciler は待機せずにスキップし、次のサイクルで再試行する
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
