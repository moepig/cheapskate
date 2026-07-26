package compute

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/aws/compute/mocks"
	"cheapskate/internal/core/model"
)

func ec2State(name ec2types.InstanceStateName) *ec2.DescribeInstancesOutput {
	return &ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{
			Instances: []ec2types.Instance{{State: &ec2types.InstanceState{Name: name}}},
		}},
	}
}

// 状態の写像がこのターゲットの契約のすべてである
// running と stopped はそのまま対応づく
// 遷移中の状態（pending・stopping・shutting-down）は "transitioning" になり、reconciler は次のサイクルで再試行する
// terminated は stopped ではなく not-found へ写像し、reconciler が亡骸を Start しないようにする
// terminated のインスタンスは終了後も 1 時間ほど Tagging API から見え続けるためである
func TestEc2DescribeStateMapping(t *testing.T) {
	cases := []struct {
		raw  ec2types.InstanceStateName
		want model.ObservedState
	}{
		{ec2types.InstanceStateNameRunning, model.StateRunning},
		{ec2types.InstanceStateNameStopped, model.StateStopped},
		{ec2types.InstanceStateNamePending, model.StateTransitioning},
		{ec2types.InstanceStateNameStopping, model.StateTransitioning},
		{ec2types.InstanceStateNameShuttingDown, model.StateTransitioning},
		{ec2types.InstanceStateNameTerminated, model.StateNotFound},
	}
	for _, tc := range cases {
		ctrl := gomock.NewController(t)
		c := mocks.NewMockEc2API(ctrl)
		c.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).Return(ec2State(tc.raw), nil)
		tgt := &Ec2InstanceTarget{Client: c}

		obs, err := tgt.Describe(context.Background(), "i-0abc123")
		require.NoError(t, err, tc.raw)
		assert.Equal(t, tc.want, obs.State, tc.raw)
	}
}

// State は EC2 のレスポンスではポインタであり、nil で返ってくる余地がある
// 状態が分からないものを running や stopped と決めつけて操作してはならないので飛ばす
// 状態の読めるインスタンスが同じレスポンスに混ざっていれば、そちらを採用する
func TestEc2DescribeSkipsInstancesWithoutState(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEc2API(ctrl)
	c.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).Return(&ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{
			{State: nil},
			{State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}},
		}}},
	}, nil)
	tgt := &Ec2InstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "i-0abc123")

	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, obs.State)
}

// 状態の読めるインスタンスが 1 つもなければ、観測できていないので not-found へ落とす
// reconciler はこれを穏当なスキップとして扱い、次のサイクルで読み直す
func TestEc2DescribeWithOnlyStatelessInstancesIsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEc2API(ctrl)
	c.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).Return(&ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{State: nil}}}},
	}, nil)
	tgt := &Ec2InstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "i-0abc123")

	require.NoError(t, err)
	assert.Equal(t, model.StateNotFound, obs.State)
}

func TestEc2DescribeEmptyReservationsIsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEc2API(ctrl)
	c.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).Return(&ec2.DescribeInstancesOutput{}, nil)
	tgt := &Ec2InstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "gone")
	require.NoError(t, err)
	assert.Equal(t, model.StateNotFound, obs.State)
}

func TestEc2DescribeNotFoundErrorCode(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEc2API(ctrl)
	c.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).
		Return(nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"})
	tgt := &Ec2InstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), "gone")
	require.NoError(t, err, "InvalidInstanceID.NotFound must convert to StateNotFound, not an error")
	assert.Equal(t, model.StateNotFound, obs.State)
}

func TestEc2DescribeOtherErrorPassesThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEc2API(ctrl)
	c.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).
		Return(nil, &smithy.GenericAPIError{Code: "UnauthorizedOperation"})
	tgt := &Ec2InstanceTarget{Client: c}

	_, err := tgt.Describe(context.Background(), "i-0abc123")
	require.Error(t, err, "non-NotFound API errors must pass through unchanged")
}

func TestEc2StopStart(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEc2API(ctrl)
	c.EXPECT().StopInstances(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *ec2.StopInstancesInput, _ ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error) {
			assert.Equal(t, []string{"i-0abc123"}, in.InstanceIds)
			return &ec2.StopInstancesOutput{}, nil
		})
	c.EXPECT().StartInstances(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *ec2.StartInstancesInput, _ ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error) {
			assert.Equal(t, []string{"i-0abc123"}, in.InstanceIds)
			return &ec2.StartInstancesOutput{}, nil
		})
	tgt := &Ec2InstanceTarget{Client: c}

	require.NoError(t, tgt.Stop(context.Background(), "i-0abc123"))
	require.NoError(t, tgt.Start(context.Background(), model.Resource{Ref: "i-0abc123"}))
}

func TestEc2Type(t *testing.T) {
	assert.Equal(t, model.TypeEc2Instance, (&Ec2InstanceTarget{}).Type())
}
