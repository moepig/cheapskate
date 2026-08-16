package compute

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"cheapskate/internal/core/model"
)

// ターゲットが使う EC2 クライアントの部分集合
type Ec2API interface {
	DescribeInstances(ctx context.Context, in *ec2.DescribeInstancesInput, opts ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error)
	StartInstances(ctx context.Context, in *ec2.StartInstancesInput, opts ...func(*ec2.Options)) (*ec2.StartInstancesOutput, error)
	StopInstances(ctx context.Context, in *ec2.StopInstancesInput, opts ...func(*ec2.Options)) (*ec2.StopInstancesOutput, error)
}

// EC2 インスタンスを管理する
// desiredCount やスケーリングに相当する設定を持たないため、Start は res.Tags を参照しない
type Ec2InstanceTarget struct {
	Client Ec2API
}

func (t *Ec2InstanceTarget) Type() model.ResourceType { return model.TypeEc2Instance }

func (t *Ec2InstanceTarget) Describe(ctx context.Context, ref string) (model.Observation, error) {
	out, err := t.Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{InstanceIds: []string{ref}})
	if err != nil {
		if isEc2NotFound(err) {
			return model.Observation{State: model.StateNotFound}, nil
		}
		return model.Observation{}, err
	}
	for _, res := range out.Reservations {
		for _, inst := range res.Instances {
			if inst.State == nil {
				continue
			}
			return ec2Observation(inst.State.Name), nil
		}
	}
	return model.Observation{State: model.StateNotFound}, nil
}

func (t *Ec2InstanceTarget) Stop(ctx context.Context, ref string) error {
	_, err := t.Client.StopInstances(ctx, &ec2.StopInstancesInput{InstanceIds: []string{ref}})
	return err
}

func (t *Ec2InstanceTarget) Start(ctx context.Context, res model.Resource) error {
	_, err := t.Client.StartInstances(ctx, &ec2.StartInstancesInput{InstanceIds: []string{res.Ref}})
	return err
}

// EC2 インスタンスの状態を cheapskate の Observation へ写像する
// terminated は stopped ではなく not-found として報告する
// terminated のインスタンスは Tagging API から 1 時間程度は返り続けるため、stopped として扱った場合、reconciler が終了済みのインスタンスに対して Start を試みる
func ec2Observation(name ec2types.InstanceStateName) model.Observation {
	switch name {
	case ec2types.InstanceStateNameRunning:
		return model.Observation{State: model.StateRunning, Detail: string(name)}
	case ec2types.InstanceStateNameStopped:
		return model.Observation{State: model.StateStopped, Detail: string(name)}
	case ec2types.InstanceStateNameTerminated:
		return model.Observation{State: model.StateNotFound, Detail: string(name)}
	default: // pending、stopping、shutting-down
		return model.Observation{State: model.StateTransitioning, Detail: string(name)}
	}
}

// err が EC2 の InvalidInstanceID.NotFound かどうかを報告する
// EC2 は RDS の DBInstanceNotFoundFault に相当する型付きの not-found を持たず、このコードを持つ smithy.APIError のみを返す
func isEc2NotFound(err error) bool {
	apiErr, ok := errors.AsType[smithy.APIError](err)
	return ok && apiErr.ErrorCode() == "InvalidInstanceID.NotFound"
}
