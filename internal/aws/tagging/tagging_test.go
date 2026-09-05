package tagging

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/aws/tagging/mocks"
	"cheapskate/internal/core/model"
)

func TestParseARN(t *testing.T) {
	tests := map[string]model.Resource{
		"arn:aws:rds:ap-northeast-1:123456789012:db:dev-db":               {Type: model.TypeRdsInstance, Ref: "dev-db", ARN: "arn:aws:rds:ap-northeast-1:123456789012:db:dev-db"},
		"arn:aws:rds:ap-northeast-1:123456789012:cluster:dev-aurora":      {Type: model.TypeRdsCluster, Ref: "dev-aurora", ARN: "arn:aws:rds:ap-northeast-1:123456789012:cluster:dev-aurora"},
		"arn:aws:ecs:ap-northeast-1:123456789012:service/dev-cluster/api": {Type: model.TypeEcsService, Ref: "dev-cluster/api", ARN: "arn:aws:ecs:ap-northeast-1:123456789012:service/dev-cluster/api"},
		"arn:aws:ec2:ap-northeast-1:123456789012:instance/i-0abc123":      {Type: model.TypeEc2Instance, Ref: "i-0abc123", ARN: "arn:aws:ec2:ap-northeast-1:123456789012:instance/i-0abc123"},
	}
	for arn, want := range tests {
		got, err := ParseARN(arn)
		require.NoError(t, err)
		assert.Equal(t, want, got)
	}
}

func TestParseARNRejectsUnsupportedForms(t *testing.T) {
	for _, arn := range []string{
		"not-an-arn",
		"arn:aws:rds:ap-northeast-1:123456789012:snapshot:dev",
		"arn:aws:ecs:ap-northeast-1:123456789012:service/api",
		"arn:aws:ec2:ap-northeast-1:123456789012:instance/not-an-id",
	} {
		assert.Error(t, func() error { _, err := ParseARN(arn); return err }(), arn)
	}
}

func TestDiscoverUsesFixedFilterAndKeepsLastARNObservation(t *testing.T) {
	controller := gomock.NewController(t)
	client := mocks.NewMockAPI(controller)
	arn := "arn:aws:rds:ap-northeast-1:123456789012:db:dev-db"
	token := "next"
	gomock.InOrder(
		client.EXPECT().GetResources(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, input *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
				require.Len(t, input.TagFilters, 1)
				assert.Equal(t, model.GroupTagKey, aws.ToString(input.TagFilters[0].Key))
				assert.Empty(t, input.TagFilters[0].Values)
				assert.ElementsMatch(t, []string{"ec2:instance", "ecs:service", "rds:cluster", "rds:db"}, input.ResourceTypeFilters)
				assert.EqualValues(t, 100, aws.ToInt32(input.ResourcesPerPage))
				return &resourcegroupstaggingapi.GetResourcesOutput{
					ResourceTagMappingList: []types.ResourceTagMapping{{ResourceARN: aws.String(arn), Tags: []types.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("old")}}}},
					PaginationToken:        aws.String(token),
				}, nil
			}),
		client.EXPECT().GetResources(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, input *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
				assert.Equal(t, token, aws.ToString(input.PaginationToken))
				return &resourcegroupstaggingapi.GetResourcesOutput{ResourceTagMappingList: []types.ResourceTagMapping{{
					ResourceARN: aws.String(arn), Tags: []types.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("new")}},
				}}}, nil
			}),
	)

	resources, err := (&Discoverer{Client: client}).Discover(context.Background())
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.Equal(t, "new", resources[arn].Tags[model.GroupTagKey])
}

func TestDiscoverFailsClosed(t *testing.T) {
	controller := gomock.NewController(t)
	client := mocks.NewMockAPI(controller)
	client.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(nil, errors.New("access denied"))

	resources, err := (&Discoverer{Client: client}).Discover(context.Background())
	assert.Nil(t, resources)
	assert.ErrorContains(t, err, "access denied")
}
