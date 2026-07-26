package compute

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/aws/compute/mocks"
	"cheapskate/internal/core/model"
)

var errOther = errors.New("some other AWS error")

// rdsObservation のステータス文字列から Observation への写像を、すべての分岐について確かめる
// "available" と "stopped" 以外はすべて transitioning となり、reconciler は待たずに次のサイクルで再試行する
func TestRdsObservationStateMapping(t *testing.T) {
	cases := []struct {
		raw  string
		want model.ObservedState
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

// Describe がエラーではなく空リストを返した場合も not-found として扱わなければならない
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

// ステータスは RDS のレスポンスでもポインタであり、nil で返ってくる余地がある
// EC2 の State と同じく飛ばす（TestEc2DescribeSkipsInstancesWithoutState と対になる）
// deref すれば reconciler は Lambda ごと panic し、そのサイクルの他のリソースまで巻き添えになる
func TestRdsInstanceDescribeSkipsInstancesWithoutStatus(t *testing.T) {
	available := "available"
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).
		Return(&rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{
			{DBInstanceStatus: nil},
			{DBInstanceStatus: &available},
		}}, nil)
	tgt := &RdsInstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "db")

	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, obs.State)
}

// 状態の読めるインスタンスが 1 つもなければ、観測できていないので not-found へ落とす
// reconciler はこれを穏当なスキップとして扱い、次のサイクルで読み直す
func TestRdsInstanceDescribeWithOnlyStatuslessInstancesIsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).
		Return(&rds.DescribeDBInstancesOutput{DBInstances: []types.DBInstance{{DBInstanceStatus: nil}}}, nil)
	tgt := &RdsInstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "db")

	require.NoError(t, err)
	assert.Equal(t, model.StateNotFound, obs.State)
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

	require.NoError(t, tgt.Stop(context.Background(), "db"))
	require.NoError(t, tgt.Start(context.Background(), model.Resource{Ref: "db"}))
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

// クラスタの状態も、インスタンスと同じ rdsObservation の写像を通らなければならない
// TestRdsInstanceDescribeRunning のクラスタ版であり、片方だけ写像を外れて壊れていないことを確かめる
func TestRdsClusterDescribeRunning(t *testing.T) {
	status := "available"
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).
		Return(&rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{{Status: &status}}}, nil)
	tgt := &RdsClusterTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "aurora")

	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, obs.State)
}

func TestRdsClusterDescribeOtherErrorPassesThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(nil, errOther)
	tgt := &RdsClusterTarget{Client: c}

	_, err := tgt.Describe(context.Background(), "cluster")
	require.ErrorIs(t, err, errOther, "non-NotFound error must pass through unchanged")
}

// nil ステータスの扱いも、インスタンスとクラスタで揃っていなければならない
// TestRdsInstanceDescribeSkipsInstancesWithoutStatus のクラスタ版である
func TestRdsClusterDescribeSkipsClustersWithoutStatus(t *testing.T) {
	available := "available"
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).
		Return(&rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{
			{Status: nil},
			{Status: &available},
		}}, nil)
	tgt := &RdsClusterTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "aurora")

	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, obs.State)
}

func TestRdsClusterDescribeWithOnlyStatuslessClustersIsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockRdsAPI(ctrl)
	c.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).
		Return(&rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{{Status: nil}}}, nil)
	tgt := &RdsClusterTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "aurora")

	require.NoError(t, err)
	assert.Equal(t, model.StateNotFound, obs.State)
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

	require.NoError(t, tgt.Stop(context.Background(), "cluster"))
	require.NoError(t, tgt.Start(context.Background(), model.Resource{Ref: "cluster"}))
}
