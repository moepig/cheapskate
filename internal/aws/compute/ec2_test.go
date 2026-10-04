package compute

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
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

// ASG に所属しないインスタンスの各状態を Describe のレスポンスへ設定し、Observation の状態を検証する。
// running と stopped はそのまま対応し、pending、stopping、shutting-down は transitioning へ写像する。
// terminated は起動対象にしないため not-found へ写像する。
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

		obs, err := tgt.Describe(context.Background(), model.Resource{Ref: "i-0abc123"})
		require.NoError(t, err, tc.raw)
		assert.Equal(t, tc.want, obs.State, tc.raw)
	}
}

// ASG 所属タグを Describe のレスポンスへ設定し、状態やタグ値によらずエラーを返すことを検証する。
func TestEc2DescribeRejectsAutoScalingGroupMembers(t *testing.T) {
	cases := []struct {
		name  string
		state *ec2types.InstanceState
		group *string
	}{
		{"running", &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}, aws.String("dev-asg")},
		{"stopped", &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopped}, aws.String("dev-asg")},
		{"pending", &ec2types.InstanceState{Name: ec2types.InstanceStateNamePending}, aws.String("dev-asg")},
		{"stopping", &ec2types.InstanceState{Name: ec2types.InstanceStateNameStopping}, aws.String("dev-asg")},
		{"shutting-down", &ec2types.InstanceState{Name: ec2types.InstanceStateNameShuttingDown}, aws.String("dev-asg")},
		{"terminated", &ec2types.InstanceState{Name: ec2types.InstanceStateNameTerminated}, aws.String("dev-asg")},
		{"without-state", nil, aws.String("dev-asg")},
		{"empty-group-name", &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}, aws.String("")},
		{"nil-group-name", &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := mocks.NewMockEc2API(gomock.NewController(t))
			client.EXPECT().DescribeInstances(gomock.Any(), &ec2.DescribeInstancesInput{InstanceIds: []string{"i-0abc123"}}).
				Return(&ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{
					State: tc.state,
					Tags: []ec2types.Tag{
						{Key: aws.String("Name"), Value: aws.String("dev")},
						{Key: aws.String("aws:autoscaling:groupName"), Value: tc.group},
					},
				}}}}}, nil)
			target := &Ec2InstanceTarget{Client: client}

			observation, err := target.Describe(context.Background(), model.Resource{Ref: "i-0abc123"})

			require.ErrorContains(t, err, "i-0abc123 is a member of Auto Scaling group")
			assert.ErrorContains(t, err, `Auto Scaling group "`+aws.ToString(tc.group)+`"`)
			assert.Equal(t, model.Observation{}, observation)
		})
	}
}

// ASG 所属タグを含まないレスポンスを設定し、通常のタグや nil のキーが状態判定を妨げないことを検証する。
func TestEc2DescribeAllowsStandaloneInstanceTags(t *testing.T) {
	client := mocks.NewMockEc2API(gomock.NewController(t))
	out := ec2State(ec2types.InstanceStateNameRunning)
	out.Reservations[0].Instances[0].Tags = []ec2types.Tag{
		{Key: nil, Value: aws.String("dev-asg")},
		{Key: aws.String("Name"), Value: aws.String("dev")},
	}
	client.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).Return(out, nil)
	target := &Ec2InstanceTarget{Client: client}

	observation, err := target.Describe(context.Background(), model.Resource{Ref: "i-0abc123"})

	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, observation.State)
}

// State は EC2 のレスポンスにおいてポインタであり、nil となりうる
// 状態を判定できないインスタンスを running や stopped として操作してはならないため、スキップする
// 同じレスポンスに状態を読めるインスタンスが含まれる場合は、そちらを採用する
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

	obs, err := tgt.Describe(context.Background(), model.Resource{Ref: "i-0abc123"})

	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, obs.State)
}

// 状態を読めるインスタンスが 1 つも存在しない場合は、観測できていないため not-found とする
// reconciler はこれをスキップとして扱い、次のサイクルで再度読み取る
func TestEc2DescribeWithOnlyStatelessInstancesIsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEc2API(ctrl)
	c.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).Return(&ec2.DescribeInstancesOutput{
		Reservations: []ec2types.Reservation{{Instances: []ec2types.Instance{{State: nil}}}},
	}, nil)
	tgt := &Ec2InstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), model.Resource{Ref: "i-0abc123"})

	require.NoError(t, err)
	assert.Equal(t, model.StateNotFound, obs.State)
}

func TestEc2DescribeEmptyReservationsIsNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEc2API(ctrl)
	c.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).Return(&ec2.DescribeInstancesOutput{}, nil)
	tgt := &Ec2InstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), model.Resource{Ref: "gone"})
	require.NoError(t, err)
	assert.Equal(t, model.StateNotFound, obs.State)
}

func TestEc2DescribeNotFoundErrorCode(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEc2API(ctrl)
	c.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).
		Return(nil, &smithy.GenericAPIError{Code: "InvalidInstanceID.NotFound"})
	tgt := &Ec2InstanceTarget{Client: c}

	obs, err := tgt.Describe(context.Background(), model.Resource{Ref: "gone"})
	require.NoError(t, err, "InvalidInstanceID.NotFound must convert to StateNotFound, not an error")
	assert.Equal(t, model.StateNotFound, obs.State)
}

func TestEc2DescribeOtherErrorPassesThrough(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEc2API(ctrl)
	c.EXPECT().DescribeInstances(gomock.Any(), gomock.Any()).
		Return(nil, &smithy.GenericAPIError{Code: "UnauthorizedOperation"})
	tgt := &Ec2InstanceTarget{Client: c}

	_, err := tgt.Describe(context.Background(), model.Resource{Ref: "i-0abc123"})
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

	require.NoError(t, tgt.Stop(context.Background(), model.Resource{Ref: "i-0abc123"}))
	require.NoError(t, tgt.Start(context.Background(), model.Resource{Ref: "i-0abc123"}))
}

func TestEc2Type(t *testing.T) {
	assert.Equal(t, model.TypeEc2Instance, (&Ec2InstanceTarget{}).Type())
}
