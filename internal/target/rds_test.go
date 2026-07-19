package target

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/mocks"
	"cheapskate/internal/model"
)

var errOther = errors.New("some other AWS error")

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
		assert.Equal(t, tc.want, obs.State, tc.raw)
		assert.Equal(t, tc.raw, obs.Detail, tc.raw)
	}
}

func TestRdsInstanceDescribeNotFoundFault(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).Return(nil, &types.DBInstanceNotFoundFault{})
	tgt := &RdsInstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "gone")
	require.NoError(t, err, "NotFoundFault must convert to StateNotFound, not an error")
	assert.Equal(t, model.StateNotFound, obs.State)
}

// DESIGN.md: Describe returning an empty list (rather than an error) must also be treated as not-found.
func TestRdsInstanceDescribeEmptyListIsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBInstancesOutput{}, nil)
	tgt := &RdsInstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "gone")
	require.NoError(t, err)
	assert.Equal(t, model.StateNotFound, obs.State)
}

func TestRdsInstanceDescribeOtherErrorPassesThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).Return(nil, errOther)
	tgt := &RdsInstanceTarget{Client: c}

	_, err := tgt.Describe(context.Background(), "db")
	require.ErrorIs(t, err, errOther, "non-NotFound error must pass through unchanged")
}

func TestRdsInstanceDescribeRunning(t *testing.T) {
	status := "available"
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).
		Return(&rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{DBInstanceStatus: &status}}}, nil)
	tgt := &RdsInstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "db")
	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, obs.State)
}

func TestRdsInstanceStopStart(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().StopDBInstance(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *rds.StopDBInstanceInput, _ ...func(*rds.Options)) (*rds.StopDBInstanceOutput, error) {
			assert.Equal(t, "db", *in.DBInstanceIdentifier)
			return &rds.StopDBInstanceOutput{}, nil
		})
	c.EXPECT().StartDBInstance(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *rds.StartDBInstanceInput, _ ...func(*rds.Options)) (*rds.StartDBInstanceOutput, error) {
			assert.Equal(t, "db", *in.DBInstanceIdentifier)
			return &rds.StartDBInstanceOutput{}, nil
		})
	tgt := &RdsInstanceTarget{Client: c}

	require.NoError(t, tgt.Stop(context.Background(), "db", model.Member{}, model.Status{}))
	_, err := tgt.Start(context.Background(), "db", model.Member{}, model.Status{})
	require.NoError(t, err)

	saved, err := tgt.PrepareStop(context.Background(), "db", model.Member{}, model.Status{})
	require.NoError(t, err)
	assert.Nil(t, saved, "RDS has nothing to save before stop")
}

func TestRdsClusterDescribeNotFoundFault(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(nil, &types.DBClusterNotFoundFault{})
	tgt := &RdsClusterTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "gone")
	require.NoError(t, err, "NotFoundFault must convert to StateNotFound, not an error")
	assert.Equal(t, model.StateNotFound, obs.State)
}

func TestRdsClusterDescribeEmptyListIsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBClustersOutput{}, nil)
	tgt := &RdsClusterTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "gone")
	require.NoError(t, err)
	assert.Equal(t, model.StateNotFound, obs.State)
}

func TestRdsClusterDescribeOtherErrorPassesThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(nil, errOther)
	tgt := &RdsClusterTarget{Client: c}

	_, err := tgt.Describe(context.Background(), "cluster")
	require.ErrorIs(t, err, errOther, "non-NotFound error must pass through unchanged")
}

func TestRdsClusterStopStart(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().StopDBCluster(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *rds.StopDBClusterInput, _ ...func(*rds.Options)) (*rds.StopDBClusterOutput, error) {
			assert.Equal(t, "cluster", *in.DBClusterIdentifier)
			return &rds.StopDBClusterOutput{}, nil
		})
	c.EXPECT().StartDBCluster(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *rds.StartDBClusterInput, _ ...func(*rds.Options)) (*rds.StartDBClusterOutput, error) {
			assert.Equal(t, "cluster", *in.DBClusterIdentifier)
			return &rds.StartDBClusterOutput{}, nil
		})
	tgt := &RdsClusterTarget{Client: c}

	require.NoError(t, tgt.Stop(context.Background(), "cluster", model.Member{}, model.Status{}))
	_, err := tgt.Start(context.Background(), "cluster", model.Member{}, model.Status{})
	require.NoError(t, err)
}
