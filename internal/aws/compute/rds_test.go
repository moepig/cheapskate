package compute

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/aws/compute/mocks"
	"cheapskate/internal/core/model"
)

func TestRdsInstanceRejectsClusterMembersAndCustom(t *testing.T) {
	for name, instance := range map[string]types.DBInstance{
		"cluster member": {DBInstanceIdentifier: aws.String("db"), DBInstanceStatus: aws.String("available"), Engine: aws.String("aurora-mysql"), DBClusterIdentifier: aws.String("cluster")},
		"RDS Custom":     {DBInstanceIdentifier: aws.String("db"), DBInstanceStatus: aws.String("available"), Engine: aws.String("custom-oracle-ee")},
	} {
		t.Run(name, func(t *testing.T) {
			client := mocks.NewMockRdsAPI(gomock.NewController(t))
			client.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{instance}}, nil)
			_, err := (&RdsInstanceTarget{Client: client}).Describe(context.Background(), model.Resource{Ref: "db"})
			assert.Error(t, err)
		})
	}
}

func TestRdsInstanceAcceptsStandaloneDatabase(t *testing.T) {
	client := mocks.NewMockRdsAPI(gomock.NewController(t))
	client.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
		DBInstanceIdentifier: aws.String("db"), DBInstanceStatus: aws.String("available"), Engine: aws.String("postgres"),
	}}}, nil)
	observation, err := (&RdsInstanceTarget{Client: client}).Describe(context.Background(), model.Resource{Ref: "db"})
	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, observation.State)
}

func TestRdsClusterAcceptsOnlyAurora(t *testing.T) {
	for engine, wantErr := range map[string]bool{"aurora-mysql": false, "aurora-postgresql": false, "mysql": true} {
		t.Run(engine, func(t *testing.T) {
			client := mocks.NewMockRdsAPI(gomock.NewController(t))
			client.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{{
				DBClusterIdentifier: aws.String("cluster"), Status: aws.String("stopped"), Engine: aws.String(engine),
			}}}, nil)
			observation, err := (&RdsClusterTarget{Client: client}).Describe(context.Background(), model.Resource{Ref: "cluster"})
			if wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, model.StateStopped, observation.State)
		})
	}
}

func TestRdsTargetsStartAndStop(t *testing.T) {
	client := mocks.NewMockRdsAPI(gomock.NewController(t))
	client.EXPECT().StopDBInstance(gomock.Any(), gomock.Any()).Return(&rds.StopDBInstanceOutput{}, nil)
	client.EXPECT().StartDBInstance(gomock.Any(), gomock.Any()).Return(&rds.StartDBInstanceOutput{}, nil)
	target := &RdsInstanceTarget{Client: client}
	resource := model.Resource{Ref: "db"}
	require.NoError(t, target.Stop(context.Background(), resource))
	require.NoError(t, target.Start(context.Background(), resource))
}

func TestRdsTargetsUseResourceIdentifiersForEveryOperation(t *testing.T) {
	controller := gomock.NewController(t)
	client := mocks.NewMockRdsAPI(controller)
	instance := &RdsInstanceTarget{Client: client}
	cluster := &RdsClusterTarget{Client: client}
	resource := model.Resource{Ref: "db"}
	clusterResource := model.Resource{Ref: "aurora"}
	client.EXPECT().StopDBInstance(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, in *rds.StopDBInstanceInput, _ ...func(*rds.Options)) (*rds.StopDBInstanceOutput, error) {
		assert.Equal(t, "db", aws.ToString(in.DBInstanceIdentifier))
		return &rds.StopDBInstanceOutput{}, nil
	})
	client.EXPECT().StartDBInstance(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, in *rds.StartDBInstanceInput, _ ...func(*rds.Options)) (*rds.StartDBInstanceOutput, error) {
		assert.Equal(t, "db", aws.ToString(in.DBInstanceIdentifier))
		return &rds.StartDBInstanceOutput{}, nil
	})
	client.EXPECT().StopDBCluster(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, in *rds.StopDBClusterInput, _ ...func(*rds.Options)) (*rds.StopDBClusterOutput, error) {
		assert.Equal(t, "aurora", aws.ToString(in.DBClusterIdentifier))
		return &rds.StopDBClusterOutput{}, nil
	})
	client.EXPECT().StartDBCluster(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, in *rds.StartDBClusterInput, _ ...func(*rds.Options)) (*rds.StartDBClusterOutput, error) {
		assert.Equal(t, "aurora", aws.ToString(in.DBClusterIdentifier))
		return &rds.StartDBClusterOutput{}, nil
	})

	require.NoError(t, instance.Stop(context.Background(), resource))
	require.NoError(t, instance.Start(context.Background(), resource))
	require.NoError(t, cluster.Stop(context.Background(), clusterResource))
	require.NoError(t, cluster.Start(context.Background(), clusterResource))
}

func TestRdsInstanceDescribeHandlesNotFoundNilStatusAndAPIFailure(t *testing.T) {
	for name, test := range map[string]struct {
		output    *rds.DescribeDBInstancesOutput
		apiErr    error
		wantState model.ObservedState
	}{
		"not found":  {apiErr: &types.DBInstanceNotFoundFault{}, wantState: model.StateNotFound},
		"nil status": {output: &rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{DBInstanceIdentifier: aws.String("db")}}}, wantState: model.StateNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			client := mocks.NewMockRdsAPI(gomock.NewController(t))
			client.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).Return(test.output, test.apiErr)
			observation, err := (&RdsInstanceTarget{Client: client}).Describe(context.Background(), model.Resource{Ref: "db"})
			require.NoError(t, err)
			assert.Equal(t, test.wantState, observation.State)
		})
	}
	client := mocks.NewMockRdsAPI(gomock.NewController(t))
	client.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).Return(nil, errors.New("RDS unavailable"))
	_, err := (&RdsInstanceTarget{Client: client}).Describe(context.Background(), model.Resource{Ref: "db"})
	assert.ErrorContains(t, err, "RDS unavailable")
}

func TestRdsClusterDescribeHandlesNotFoundAndAPIFailure(t *testing.T) {
	client := mocks.NewMockRdsAPI(gomock.NewController(t))
	client.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(nil, &types.DBClusterNotFoundFault{})
	observation, err := (&RdsClusterTarget{Client: client}).Describe(context.Background(), model.Resource{Ref: "cluster"})
	require.NoError(t, err)
	assert.Equal(t, model.StateNotFound, observation.State)

	client = mocks.NewMockRdsAPI(gomock.NewController(t))
	client.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(nil, errors.New("RDS cluster unavailable"))
	_, err = (&RdsClusterTarget{Client: client}).Describe(context.Background(), model.Resource{Ref: "cluster"})
	assert.ErrorContains(t, err, "RDS cluster unavailable")
}
