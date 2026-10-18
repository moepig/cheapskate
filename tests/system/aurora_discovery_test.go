//go:build integration

package system

import (
	"context"
	"log/slog"
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

	"cheapskate/internal/aws/tagging"
	taggingmocks "cheapskate/internal/aws/tagging/mocks"
	"cheapskate/internal/core/model"
	"cheapskate/internal/devtools/emutest"
)

// Floci の RDS API でクラスタを取得し、所属タグのない応答と識別子を絞り込めない応答から操作対象を生成しないことを確認する。
// 使用する 1.7.0 の XML にはクラスタの TagList がなく、フィルタは db-cluster-id のみを解釈する。
// https://github.com/floci-io/floci/blob/75c8cfc4d93b62063b6a435dff0fc77df180ff84/src/main/java/io/github/hectorvent/floci/services/rds/RdsQueryHandler.java
// "filterId = extractRdsFilterValue(params, \"db-cluster-id\");"
func TestAuroraDiscoveryRejectsUnverifiedFlociResponses(t *testing.T) {
	cfg := emutest.Config(t)
	client := rds.NewFromConfig(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	group := emutest.RandomName("aurora")
	var clusters []types.DBCluster
	for _, engine := range []string{"aurora-mysql", "aurora-postgresql"} {
		name := emutest.RandomName("aurora-discovery")
		out, err := client.CreateDBCluster(ctx, &rds.CreateDBClusterInput{
			DBClusterIdentifier: aws.String(name), Engine: aws.String(engine),
			MasterUsername: aws.String("testuser"), MasterUserPassword: aws.String("local-test-password"),
			Tags: []types.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String(group)}},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cleanupCancel()
			_, err := client.DeleteDBCluster(cleanupCtx, &rds.DeleteDBClusterInput{DBClusterIdentifier: aws.String(name), SkipFinalSnapshot: aws.Bool(true)})
			assert.NoError(t, err)
		})
		require.NotNil(t, out.DBCluster)
		_, err = client.AddTagsToResource(ctx, &rds.AddTagsToResourceInput{
			ResourceName: out.DBCluster.DBClusterArn, Tags: []types.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String(group)}},
		})
		require.NoError(t, err)
		tags, err := client.ListTagsForResource(ctx, &rds.ListTagsForResourceInput{ResourceName: out.DBCluster.DBClusterArn})
		require.NoError(t, err)
		assert.Contains(t, tags.TagList, types.Tag{Key: aws.String(model.GroupTagKey), Value: aws.String(group)})
		clusters = append(clusters, *out.DBCluster)
	}
	log := slog.New(slog.DiscardHandler)
	for _, aliases := range []bool{false, true} {
		name := "missing membership tags"
		if aliases {
			name = "unfiltered resource ID query"
		}
		t.Run(name, func(t *testing.T) {
			var mappings []tagtypes.ResourceTagMapping
			for _, cluster := range clusters {
				canonical := aws.ToString(cluster.DBClusterArn)
				resourceARN := canonical
				if aliases {
					id := aws.ToString(cluster.DbClusterResourceId)
					require.NotEmpty(t, id)
					resourceARN = canonical[:len(canonical)-len(aws.ToString(cluster.DBClusterIdentifier))] + id
				}
				mappings = append(mappings, tagtypes.ResourceTagMapping{
					ResourceARN: aws.String(resourceARN), Tags: []tagtypes.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String(group)}},
				})
			}
			tags := taggingmocks.NewMockAPI(gomock.NewController(t))
			tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{ResourceTagMappingList: mappings}, nil)

			resources, err := (&tagging.Discoverer{Client: tags, Rds: client, Log: log}).Discover(ctx)

			if aliases {
				assert.ErrorContains(t, err, "clusters for")
				assert.Nil(t, resources)
			} else {
				require.NoError(t, err)
				assert.Empty(t, resources)
			}
		})
	}
}
