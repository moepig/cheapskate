package reconcile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/app/groups"
	"cheapskate/internal/app/port"
	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/aws/compute"
	computemocks "cheapskate/internal/aws/compute/mocks"
	"cheapskate/internal/aws/tagging"
	taggingmocks "cheapskate/internal/aws/tagging/mocks"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	statemocks "cheapskate/internal/state/mocks"
)

// 名前と resource ID の二重検出を実際の探索・グループ参照・RDS adapter に通す。
// ARN の末尾が名前または resource ID のどちらでも、表示 1 件とクラスタ名による起動・停止 1 回を確認する。
func TestAuroraAliasesProduceOneRowAndOneCanonicalAction(t *testing.T) {
	const name = "foo"
	const resourceID = "cluster-1234567890123456"
	for _, tc := range []struct {
		desired model.Override
		arnRef  string
	}{
		{model.OverrideRunning, name}, {model.OverrideStopped, name},
		{model.OverrideRunning, resourceID}, {model.OverrideStopped, resourceID},
	} {
		t.Run(fmt.Sprintf("%s/arn=%s", tc.desired, tc.arnRef), func(t *testing.T) {
			desired := tc.desired
			controller := gomock.NewController(t)
			db, _ := statemocks.NewDynaStore(controller)
			store := state.New(db, "table")
			ctx := context.Background()
			now := time.Now()
			require.NoError(t, store.CreateGroup(ctx, model.GroupSpec{Name: "dev", Override: desired}))

			const prefix = "arn:aws:rds:ap-northeast-1:123456789012:cluster:"
			tags := taggingmocks.NewMockAPI(controller)
			tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Times(2).Return(&resourcegroupstaggingapi.GetResourcesOutput{
				ResourceTagMappingList: []tagtypes.ResourceTagMapping{
					{ResourceARN: aws.String(prefix + name), Tags: []tagtypes.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("old")}}},
					{ResourceARN: aws.String(prefix + resourceID), Tags: []tagtypes.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("other")}}},
				},
			}, nil)
			status := "available"
			wantObserved := model.StateRunning
			if desired == model.OverrideRunning {
				status = "stopped"
				wantObserved = model.StateStopped
			}
			cluster := types.DBCluster{
				DBClusterIdentifier: aws.String(name), DBClusterArn: aws.String(prefix + tc.arnRef), DbClusterResourceId: aws.String(resourceID),
				Engine: aws.String("aurora-postgresql"), Status: aws.String(status),
				TagList: []types.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("dev")}},
			}
			rdsClient := computemocks.NewMockRdsAPI(controller)
			rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
				func(_ context.Context, input *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
					ref := aws.ToString(input.DBClusterIdentifier)
					if ref == resourceID || (ref == prefix+resourceID && tc.arnRef != resourceID) {
						return nil, &types.DBClusterNotFoundFault{}
					}
					if input.DBClusterIdentifier == nil {
						assert.Equal(t, []types.Filter{{Name: aws.String("db-cluster-resource-id"), Values: []string{resourceID}}}, input.Filters)
					} else {
						assert.Contains(t, []string{name, prefix + tc.arnRef}, ref)
						assert.Empty(t, input.Filters)
					}
					return &rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{cluster}}, nil
				})
			wantAction := model.ActionStop
			if desired == model.OverrideRunning {
				wantAction = model.ActionStart
				rdsClient.EXPECT().StartDBCluster(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, input *rds.StartDBClusterInput, _ ...func(*rds.Options)) (*rds.StartDBClusterOutput, error) {
						assert.Equal(t, name, aws.ToString(input.DBClusterIdentifier))
						return &rds.StartDBClusterOutput{}, nil
					})
			} else {
				rdsClient.EXPECT().StopDBCluster(gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ context.Context, input *rds.StopDBClusterInput, _ ...func(*rds.Options)) (*rds.StopDBClusterOutput, error) {
						assert.Equal(t, name, aws.ToString(input.DBClusterIdentifier))
						return &rds.StopDBClusterOutput{}, nil
					})
			}
			discoverer := &tagging.Discoverer{Client: tags, Rds: rdsClient}
			target := &compute.RdsClusterTarget{Client: rdsClient}
			service := groups.New(store, discoverer, map[model.ResourceType]port.Describer{model.TypeRdsCluster: target}, time.UTC)

			detail, err := service.Show(ctx, "dev", now)
			require.NoError(t, err)
			require.Len(t, detail.Resources, 1)
			assert.Equal(t, name, detail.Resources[0].Resource.Ref)
			assert.Equal(t, prefix+tc.arnRef, detail.Resources[0].Resource.ARN)
			require.NoError(t, detail.Resources[0].LiveErr)
			require.NotNil(t, detail.Resources[0].Live)
			assert.Equal(t, wantObserved, detail.Resources[0].Live.State)

			notifier := &porttest.Notifier{}
			summary, err := Run(ctx, nil, &Deps{
				Store: store, Discoverer: discoverer, Targets: map[model.ResourceType]port.Target{model.TypeRdsCluster: target},
				Notifier: notifier, Location: time.UTC,
			}, now)
			require.NoError(t, err)
			assert.Empty(t, summary.Errors)
			assert.Equal(t, 1, summary.Reconciled)
			require.Len(t, summary.Actions, 1)
			assert.Equal(t, "rds-cluster#"+name, summary.Actions[0].ResourceID)
			assert.Equal(t, wantAction, summary.Actions[0].Action)
			assert.Len(t, notifier.Published, 1)
		})
	}
}

// 先行クラスタの正規化成功後に照合を失敗させ、実際の探索処理から reconcile 全体の中断を検証する。
// 全種別の状態取得・操作および通知は、探索全体の成功まで開始してはいけない。
func TestAuroraResolutionFailureDiscardsPartialDiscoveryBeforeOperations(t *testing.T) {
	for _, phase := range []string{"name query", "resource ID query", "later page"} {
		t.Run(phase, func(t *testing.T) {
			controller := gomock.NewController(t)
			const prefix = "arn:aws:rds:ap-northeast-1:123456789012:cluster:"
			const failedID = "cluster-ZZZZZZZZZZZZZZZZZZZZZZZZZZ"
			tags := taggingmocks.NewMockAPI(controller)
			tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
				ResourceTagMappingList: []tagtypes.ResourceTagMapping{
					{ResourceARN: aws.String(prefix + "aaa-ready")}, {ResourceARN: aws.String(prefix + failedID)},
					{ResourceARN: aws.String("arn:aws:ec2:ap-northeast-1:123456789012:instance/i-0123"), Tags: []tagtypes.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("dev")}}},
				},
			}, nil)
			rdsClient := computemocks.NewMockRdsAPI(controller)
			cluster := types.DBCluster{
				DBClusterIdentifier: aws.String("aaa-ready"), DBClusterArn: aws.String(prefix + "aaa-ready"),
				Engine: aws.String("aurora-mysql"), Status: aws.String("available"),
				TagList: []types.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("dev")}},
			}
			failure := errors.New("RDS resolution unavailable")
			calls := []any{
				rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Eq(&rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(prefix + "aaa-ready")})).Return(&rds.DescribeDBClustersOutput{DBClusters: []types.DBCluster{cluster}}, nil),
				rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Eq(&rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(prefix + failedID)})).Return(nil, &types.DBClusterNotFoundFault{}),
			}
			nameQuery := rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Eq(&rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(failedID)}))
			if phase == "name query" {
				calls = append(calls, nameQuery.Return(nil, failure))
			} else {
				calls = append(calls, nameQuery.Return(nil, &types.DBClusterNotFoundFault{}))
				filter := []types.Filter{{Name: aws.String("db-cluster-resource-id"), Values: []string{failedID}}}
				filterQuery := rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Eq(&rds.DescribeDBClustersInput{Filters: filter}))
				if phase == "resource ID query" {
					calls = append(calls, filterQuery.Return(nil, failure))
				} else {
					calls = append(calls, filterQuery.Return(&rds.DescribeDBClustersOutput{Marker: aws.String("next")}, nil))
					calls = append(calls, rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Eq(&rds.DescribeDBClustersInput{Filters: filter, Marker: aws.String("next")})).Return(nil, failure))
				}
			}
			gomock.InOrder(calls...)
			ec2Client := computemocks.NewMockEc2API(controller)
			notifier := &porttest.Notifier{}
			store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideStopped}}}}

			summary, err := Run(context.Background(), nil, &Deps{
				Store: store, Discoverer: &tagging.Discoverer{Client: tags, Rds: rdsClient},
				Targets: map[model.ResourceType]port.Target{
					model.TypeRdsCluster: &compute.RdsClusterTarget{Client: rdsClient}, model.TypeEc2Instance: &compute.Ec2InstanceTarget{Client: ec2Client},
				},
				Notifier: notifier, Location: time.UTC,
			}, time.Now())

			require.ErrorIs(t, err, failure)
			assert.ErrorContains(t, err, prefix+failedID)
			assert.Zero(t, summary.Reconciled)
			assert.Empty(t, summary.Actions)
			assert.Empty(t, notifier.Published)
		})
	}
}
