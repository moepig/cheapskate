package compute

import (
	"context"
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
			_, err := (&RdsInstanceTarget{Client: client}).Describe(context.Background(), "db")
			assert.Error(t, err)
		})
	}
}

func TestRdsInstanceAcceptsStandaloneDatabase(t *testing.T) {
	client := mocks.NewMockRdsAPI(gomock.NewController(t))
	client.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{
		DBInstanceIdentifier: aws.String("db"), DBInstanceStatus: aws.String("available"), Engine: aws.String("postgres"),
	}}}, nil)
	observation, err := (&RdsInstanceTarget{Client: client}).Describe(context.Background(), "db")
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
			observation, err := (&RdsClusterTarget{Client: client}).Describe(context.Background(), "cluster")
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
