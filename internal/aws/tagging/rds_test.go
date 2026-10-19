package tagging

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/aws/tagging/mocks"
	"cheapskate/internal/core/model"
)

const clusterResourceID = "cluster-ANPAJ4AE5446DAEXAMPLE"

func clusterARN(ref string) string {
	return "arn:aws:rds:ap-northeast-1:123456789012:cluster:" + ref
}

func testCluster(name string) rdstypes.DBCluster {
	return rdstypes.DBCluster{
		DBClusterIdentifier: aws.String(name), DBClusterArn: aws.String(clusterARN(name)),
		DbClusterResourceId: aws.String(clusterResourceID), Engine: aws.String("aurora-mysql"), Status: aws.String("available"),
		TagList: []rdstypes.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("dev")}},
	}
}

func taggedClusterMapping(ref string) tagtypes.ResourceTagMapping {
	return tagtypes.ResourceTagMapping{
		ResourceARN: aws.String(clusterARN(ref)),
		Tags:        []tagtypes.Tag{{Key: aws.String(model.GroupTagKey), Value: aws.String("stale-group")}},
	}
}

// 名前と resource ID の検出結果を異なる順序・ページで返し、RDS の名前・ARN・タグへ統一した 1 件を期待する。
// resource ID のみの検出と cluster- で始まる正当な名前も管理対象として維持する。
func TestDiscoverNormalizesClusterAliases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cluster string
		pages   [][]string
		calls   int
	}{
		{name: "alias before name", cluster: "dev-aurora", pages: [][]string{{clusterResourceID, "dev-aurora"}}, calls: 3},
		{name: "name before alias", cluster: "dev-aurora", pages: [][]string{{"dev-aurora", clusterResourceID}}, calls: 3},
		{name: "different pages", cluster: "dev-aurora", pages: [][]string{{clusterResourceID}, {"dev-aurora"}}, calls: 3},
		{name: "reversed pages", cluster: "dev-aurora", pages: [][]string{{"dev-aurora"}, {clusterResourceID}}, calls: 3},
		{name: "alias only", cluster: "dev-aurora", pages: [][]string{{clusterResourceID}}, calls: 3},
		{name: "name only", cluster: "dev-aurora", pages: [][]string{{"dev-aurora"}}, calls: 1},
		{name: "name resolved first", cluster: "alpha", pages: [][]string{{clusterResourceID, "alpha"}}, calls: 1},
		{name: "cluster prefix", cluster: "cluster-dev", pages: [][]string{{"cluster-dev"}}, calls: 1},
		{name: "prefix with alias", cluster: "cluster-dev", pages: [][]string{{clusterResourceID, "cluster-dev"}}, calls: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller := gomock.NewController(t)
			tags := mocks.NewMockAPI(controller)
			rdsClient := mocks.NewMockRdsAPI(controller)
			var calls []any
			for i, page := range tc.pages {
				out := &resourcegroupstaggingapi.GetResourcesOutput{}
				for _, ref := range page {
					out.ResourceTagMappingList = append(out.ResourceTagMappingList, taggedClusterMapping(ref))
				}
				if i+1 < len(tc.pages) {
					out.PaginationToken = aws.String(fmt.Sprint(i + 1))
				}
				calls = append(calls, tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(out, nil).Call)
			}
			cluster := testCluster(tc.cluster)
			calls = append(calls, rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Times(tc.calls).DoAndReturn(
				func(_ context.Context, input *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
					if input.DBClusterIdentifier != nil {
						assert.Empty(t, input.Filters)
						if *input.DBClusterIdentifier != clusterARN(tc.cluster) {
							assert.Contains(t, []string{clusterResourceID, clusterARN(clusterResourceID)}, *input.DBClusterIdentifier)
							return nil, fmt.Errorf("wrapped: %w", &rdstypes.DBClusterNotFoundFault{})
						}
					} else {
						assert.Equal(t, []rdstypes.Filter{{Name: aws.String("db-cluster-resource-id"), Values: []string{clusterResourceID}}}, input.Filters)
					}
					return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{cluster}}, nil
				}))
			gomock.InOrder(calls...)

			resources, err := (&Discoverer{Client: tags, Rds: rdsClient}).Discover(context.Background())

			require.NoError(t, err)
			require.Len(t, resources, 1)
			assert.Equal(t, model.Resource{
				Type: model.TypeRdsCluster, Ref: tc.cluster, ARN: clusterARN(tc.cluster), Tags: map[string]string{model.GroupTagKey: "dev"},
			}, resources[clusterARN(tc.cluster)])
		})
	}
}

// 大文字・小文字が異なる resource ID を返し、名前・ID の重複を同じ ARN へ統合する。
// 名前を先に解決した場合も、ID の表記差による再照会を行わない。
func TestDiscoverNormalizesClusterResourceIDCase(t *testing.T) {
	lowerID := strings.ToLower(clusterResourceID)
	mixedID := "cluster-AnPaJ4AE5446DAEXAMPLE"
	for _, tc := range []struct {
		name     string
		cluster  string
		arnRef   string
		refs     []string
		queryRef string
		calls    int
	}{
		{name: "lowercase ID", cluster: "foo", arnRef: "foo", refs: []string{lowerID}, queryRef: lowerID, calls: 3},
		{name: "mixed case ID", cluster: "foo", arnRef: "foo", refs: []string{mixedID}, queryRef: mixedID, calls: 3},
		{name: "resource ID in canonical ARN", cluster: "foo", arnRef: clusterResourceID, refs: []string{lowerID}, queryRef: lowerID, calls: 3},
		{name: "name resolved first", cluster: "alpha", arnRef: "alpha", refs: []string{"alpha", lowerID, mixedID}, calls: 1},
		{name: "multiple ID spellings", cluster: "foo", arnRef: "foo", refs: []string{lowerID, clusterResourceID, mixedID}, queryRef: clusterResourceID, calls: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller := gomock.NewController(t)
			tags := mocks.NewMockAPI(controller)
			out := &resourcegroupstaggingapi.GetResourcesOutput{}
			for _, ref := range tc.refs {
				out.ResourceTagMappingList = append(out.ResourceTagMappingList, taggedClusterMapping(ref))
			}
			tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(out, nil)
			cluster := testCluster(tc.cluster)
			cluster.DBClusterArn = aws.String(clusterARN(tc.arnRef))
			rdsClient := mocks.NewMockRdsAPI(controller)
			rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Times(tc.calls).DoAndReturn(
				func(_ context.Context, input *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
					if input.DBClusterIdentifier != nil {
						assert.Empty(t, input.Filters)
						if *input.DBClusterIdentifier != aws.ToString(cluster.DBClusterArn) {
							assert.Contains(t, []string{tc.queryRef, clusterARN(tc.queryRef)}, *input.DBClusterIdentifier)
							return nil, &rdstypes.DBClusterNotFoundFault{}
						}
					} else {
						assert.Equal(t, []rdstypes.Filter{{Name: aws.String("db-cluster-resource-id"), Values: []string{tc.queryRef}}}, input.Filters)
					}
					return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{cluster}}, nil
				})

			resources, err := (&Discoverer{Client: tags, Rds: rdsClient}).Discover(context.Background())

			require.NoError(t, err)
			assert.Equal(t, map[string]model.Resource{clusterARN(tc.arnRef): {
				Type: model.TypeRdsCluster, Ref: tc.cluster, ARN: clusterARN(tc.arnRef), Tags: map[string]string{model.GroupTagKey: "dev"},
			}}, resources)
		})
	}
}

// ARN の末尾と名前が異なる応答を返し、ARN の保持、名前による Ref、および重複候補の統合を確認する。
func TestDiscoverPreservesClusterARNWithIndependentName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cluster string
		arnRef  string
		refs    []string
		queries []string
		missing bool
	}{
		{name: "ARN only", refs: []string{clusterResourceID}},
		{name: "ARN and name", refs: []string{clusterResourceID, "foo"}},
		{name: "name and ARN", refs: []string{"foo", clusterResourceID}},
		{name: "no resource ID", refs: []string{clusterResourceID, "foo"}, missing: true},
		{name: "name alias resolved first", cluster: "alpha", refs: []string{"alpha", clusterResourceID}, queries: []string{clusterARN("alpha"), "alpha"}},
		{name: "ARN case", arnRef: "foo", refs: []string{"FOO"}, queries: []string{clusterARN("FOO")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller := gomock.NewController(t)
			tags := mocks.NewMockAPI(controller)
			out := &resourcegroupstaggingapi.GetResourcesOutput{}
			for _, ref := range tc.refs {
				out.ResourceTagMappingList = append(out.ResourceTagMappingList, taggedClusterMapping(ref))
			}
			tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(out, nil)
			name := tc.cluster
			if name == "" {
				name = "foo"
			}
			arnRef := tc.arnRef
			if arnRef == "" {
				arnRef = clusterResourceID
			}
			canonicalARN := clusterARN(arnRef)
			cluster := testCluster(name)
			cluster.DBClusterArn = aws.String(canonicalARN)
			if tc.missing {
				cluster.DbClusterResourceId = nil
			}
			rdsClient := mocks.NewMockRdsAPI(controller)
			queries := tc.queries
			if queries == nil {
				queries = []string{canonicalARN}
			}
			var calls []any
			for i, query := range queries {
				call := rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Eq(&rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(query)}))
				if i+1 < len(queries) {
					call.Return(nil, &rdstypes.DBClusterNotFoundFault{})
				} else {
					call.Return(&rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{cluster}}, nil)
				}
				calls = append(calls, call)
			}
			gomock.InOrder(calls...)

			resources, err := (&Discoverer{Client: tags, Rds: rdsClient}).Discover(context.Background())

			require.NoError(t, err)
			assert.Equal(t, map[string]model.Resource{canonicalARN: {
				Type: model.TypeRdsCluster, Ref: name, ARN: canonicalARN, Tags: map[string]string{model.GroupTagKey: "dev"},
			}}, resources)
		})
	}
}

// RDS のタグから所属を確定し、タグ削除を反映する。空のタグ値は設定エラーの判定に必要なため保持する。
func TestDiscoverUsesClusterTagsForMembership(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags []rdstypes.Tag
		keep bool
	}{
		{name: "removed"},
		{name: "unrelated", tags: []rdstypes.Tag{{Key: aws.String("Name"), Value: aws.String("dev")}}},
		{name: "empty", tags: []rdstypes.Tag{{Key: aws.String(model.GroupTagKey)}}, keep: true},
		{name: "nil key", tags: []rdstypes.Tag{{Value: aws.String("dev")}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller := gomock.NewController(t)
			tags := mocks.NewMockAPI(controller)
			tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
				ResourceTagMappingList: []tagtypes.ResourceTagMapping{taggedClusterMapping("dev-aurora")},
			}, nil)
			cluster := testCluster("dev-aurora")
			cluster.TagList = tc.tags
			rdsClient := mocks.NewMockRdsAPI(controller)
			rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{cluster}}, nil)

			resources, err := (&Discoverer{Client: tags, Rds: rdsClient}).Discover(context.Background())

			require.NoError(t, err)
			if tc.keep {
				require.Len(t, resources, 1)
				assert.Equal(t, map[string]string{model.GroupTagKey: ""}, resources[clusterARN("dev-aurora")].Tags)
			} else {
				assert.Empty(t, resources)
			}
		})
	}
}

// クラスタが存在しない候補を除外し、同じ探索で取得した他種別のリソースを維持する。
func TestDiscoverSkipsMissingClusters(t *testing.T) {
	for _, ref := range []string{"deleted", clusterResourceID} {
		t.Run(ref, func(t *testing.T) {
			controller := gomock.NewController(t)
			tags := mocks.NewMockAPI(controller)
			tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
				ResourceTagMappingList: []tagtypes.ResourceTagMapping{
					taggedClusterMapping(ref), {ResourceARN: aws.String("arn:aws:ec2:ap-northeast-1:123456789012:instance/i-0123")},
				},
			}, nil)
			rdsClient := mocks.NewMockRdsAPI(controller)
			rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Times(2).Return(nil, &rdstypes.DBClusterNotFoundFault{})
			if ref == clusterResourceID {
				rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBClustersOutput{}, nil)
			}
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))

			resources, err := (&Discoverer{Client: tags, Rds: rdsClient, Log: log}).Discover(context.Background())

			require.NoError(t, err)
			require.Len(t, resources, 1)
			for _, resource := range resources {
				assert.Equal(t, model.TypeEc2Instance, resource.Type)
			}
			var record map[string]any
			require.NoError(t, json.Unmarshal(logs.Bytes(), &record))
			assert.Equal(t, "resource-discovery-skipped", record["msg"])
			assert.Equal(t, clusterARN(ref), record["resource_arn"])
			assert.Equal(t, "not-found", record["reason"])
		})
	}
}

// RDS の参照失敗と識別子の不整合では探索全体を失敗させ、操作可能な部分結果を返さない。
func TestDiscoverRejectsClusterResolutionFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*rdstypes.DBCluster)
		apiErr   error
		fallback bool
		want     string
	}{
		{name: "access denied", apiErr: errors.New("AccessDenied"), want: "AccessDenied"},
		{name: "timeout", apiErr: context.DeadlineExceeded, want: "deadline exceeded"},
		{name: "missing ARN", change: func(c *rdstypes.DBCluster) { c.DBClusterArn = nil }, want: "different cluster ARN"},
		{name: "missing name", change: func(c *rdstypes.DBCluster) { c.DBClusterIdentifier = nil }, want: "ref is required"},
		{name: "invalid name", change: func(c *rdstypes.DBCluster) { c.DBClusterIdentifier = aws.String("other/invalid") }, want: "invalid DBClusterIdentifier"},
		{name: "different name in fallback", change: func(c *rdstypes.DBCluster) { c.DBClusterIdentifier = aws.String("other") }, fallback: true, want: "different cluster identifier"},
		{name: "ARN mismatch", change: func(c *rdstypes.DBCluster) { c.DBClusterArn = aws.String(clusterARN("other")) }, want: "different cluster ARN"},
		{name: "wrong type", change: func(c *rdstypes.DBCluster) {
			c.DBClusterArn = aws.String("arn:aws:rds:ap-northeast-1:123456789012:db:dev-aurora")
		}, want: "different cluster ARN"},
		{name: "other region", change: func(c *rdstypes.DBCluster) {
			c.DBClusterArn = aws.String("arn:aws:rds:us-east-1:123456789012:cluster:dev-aurora")
		}, want: "different cluster ARN"},
		{name: "other account", change: func(c *rdstypes.DBCluster) {
			c.DBClusterArn = aws.String("arn:aws:rds:ap-northeast-1:999999999999:cluster:dev-aurora")
		}, want: "different cluster ARN"},
		{name: "other partition", change: func(c *rdstypes.DBCluster) {
			c.DBClusterArn = aws.String("arn:aws-cn:rds:ap-northeast-1:123456789012:cluster:dev-aurora")
		}, want: "different cluster ARN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			controller := gomock.NewController(t)
			tags := mocks.NewMockAPI(controller)
			tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
				ResourceTagMappingList: []tagtypes.ResourceTagMapping{taggedClusterMapping("dev-aurora")},
			}, nil)
			cluster := testCluster("dev-aurora")
			if tc.change != nil {
				tc.change(&cluster)
			}
			rdsClient := mocks.NewMockRdsAPI(controller)
			if tc.fallback {
				rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Eq(&rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterARN("dev-aurora"))})).Return(nil, &rdstypes.DBClusterNotFoundFault{})
			}
			rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{cluster}}, tc.apiErr)

			resources, err := (&Discoverer{Client: tags, Rds: rdsClient}).Discover(context.Background())

			assert.Nil(t, resources)
			assert.ErrorContains(t, err, tc.want)
			assert.ErrorContains(t, err, clusterARN("dev-aurora"))
		})
	}
}

// resource ID の照会でページを追跡し、ID の不一致・欠落、複数クラスタ、後続ページの失敗を拒否する。
// ID の照合エラーには、要求 ID と返却された ID・クラスタ名・ARN を含める。
func TestDiscoverValidatesResourceIDQueryPages(t *testing.T) {
	for _, scenario := range []string{"success", "different ID", "missing ID", "empty ID", "multiple clusters", "page failure", "repeated marker"} {
		t.Run(scenario, func(t *testing.T) {
			controller := gomock.NewController(t)
			tags := mocks.NewMockAPI(controller)
			tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
				ResourceTagMappingList: []tagtypes.ResourceTagMapping{taggedClusterMapping(clusterResourceID)},
			}, nil)
			rdsClient := mocks.NewMockRdsAPI(controller)
			cluster := testCluster("dev-aurora")
			switch scenario {
			case "different ID":
				cluster.DbClusterResourceId = aws.String("cluster-OTHER")
			case "missing ID":
				cluster.DbClusterResourceId = nil
			case "empty ID":
				cluster.DbClusterResourceId = aws.String("")
			}
			calls := 0
			rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Times(4).DoAndReturn(
				func(_ context.Context, input *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
					calls++
					if calls == 1 {
						assert.Equal(t, clusterARN(clusterResourceID), aws.ToString(input.DBClusterIdentifier))
						return nil, &rdstypes.DBClusterNotFoundFault{}
					}
					if calls == 2 {
						assert.Equal(t, clusterResourceID, aws.ToString(input.DBClusterIdentifier))
						return &rds.DescribeDBClustersOutput{}, nil
					}
					assert.Nil(t, input.DBClusterIdentifier)
					assert.Equal(t, []rdstypes.Filter{{Name: aws.String("db-cluster-resource-id"), Values: []string{clusterResourceID}}}, input.Filters)
					if calls == 3 {
						assert.Nil(t, input.Marker)
						return &rds.DescribeDBClustersOutput{Marker: aws.String("next")}, nil
					}
					assert.Equal(t, "next", aws.ToString(input.Marker))
					out := &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{cluster}}
					switch scenario {
					case "multiple clusters":
						out.DBClusters = append(out.DBClusters, testCluster("other"))
					case "page failure":
						return nil, errors.New("page failed")
					case "repeated marker":
						out.Marker = aws.String("next")
					}
					return out, nil
				})

			resources, err := (&Discoverer{Client: tags, Rds: rdsClient}).Discover(context.Background())

			if scenario == "success" {
				require.NoError(t, err)
				require.Len(t, resources, 1)
				assert.Equal(t, "dev-aurora", resources[clusterARN("dev-aurora")].Ref)
			} else {
				assert.Error(t, err)
				assert.Nil(t, resources)
				if strings.HasSuffix(scenario, "ID") {
					assert.ErrorContains(t, err, fmt.Sprintf("different resource ID for %q: got %q", clusterResourceID, aws.ToString(cluster.DbClusterResourceId)))
					assert.ErrorContains(t, err, `DBClusterIdentifier="dev-aurora"`)
					assert.ErrorContains(t, err, fmt.Sprintf("DBClusterArn=%q", clusterARN("dev-aurora")))
				}
			}
		})
	}
}

// 探索を繰り返して RDS の名前・タグを変更し、前回の対応関係が再利用されないことを確認する。
func TestDiscoverRefreshesClusterIdentityAndTags(t *testing.T) {
	controller := gomock.NewController(t)
	tags := mocks.NewMockAPI(controller)
	tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Times(2).Return(&resourcegroupstaggingapi.GetResourcesOutput{
		ResourceTagMappingList: []tagtypes.ResourceTagMapping{taggedClusterMapping(clusterResourceID)},
	}, nil)
	rdsClient := mocks.NewMockRdsAPI(controller)
	for _, name := range []string{"before", "after"} {
		cluster := testCluster(name)
		cluster.TagList[0].Value = aws.String(name)
		rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Times(2).Return(nil, &rdstypes.DBClusterNotFoundFault{})
		rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{cluster}}, nil)
	}
	discoverer := &Discoverer{Client: tags, Rds: rdsClient}
	for _, name := range []string{"before", "after"} {
		resources, err := discoverer.Discover(context.Background())
		require.NoError(t, err)
		require.Len(t, resources, 1)
		assert.Equal(t, name, resources[clusterARN(name)].Ref)
		assert.Equal(t, name, resources[clusterARN(name)].Tags[model.GroupTagKey])
	}
}

// 異なるクラスタと各エイリアスを複数ページに混在させ、クラスタごとの名前・ARN・所属タグを維持する。
func TestDiscoverKeepsDistinctClustersWithAliases(t *testing.T) {
	const otherID = "cluster-BBBBBBBBBBBBBBBBBBBBBBBBBB"
	for _, pages := range [][][]string{
		{{clusterResourceID, "omega"}, {otherID, "alpha"}},
		{{"alpha", otherID}, {"omega", clusterResourceID}},
	} {
		t.Run(fmt.Sprint(pages), func(t *testing.T) {
			controller := gomock.NewController(t)
			tags := mocks.NewMockAPI(controller)
			var calls []any
			for i, page := range pages {
				out := &resourcegroupstaggingapi.GetResourcesOutput{}
				for _, ref := range page {
					out.ResourceTagMappingList = append(out.ResourceTagMappingList, taggedClusterMapping(ref))
				}
				if i+1 < len(pages) {
					out.PaginationToken = aws.String("next")
				}
				calls = append(calls, tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(out, nil).Call)
			}
			gomock.InOrder(calls...)
			alpha, omega := testCluster("alpha"), testCluster("omega")
			omega.DbClusterResourceId = aws.String(otherID)
			omega.TagList[0].Value = aws.String("other-group")
			clusters := map[string]rdstypes.DBCluster{"alpha": alpha, "omega": omega, clusterResourceID: alpha, otherID: omega}
			rdsClient := mocks.NewMockRdsAPI(controller)
			rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
				func(_ context.Context, input *rds.DescribeDBClustersInput, _ ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error) {
					ref := aws.ToString(input.DBClusterIdentifier)
					ref = strings.TrimPrefix(ref, clusterARN(""))
					if ref == clusterResourceID || ref == otherID {
						return nil, &rdstypes.DBClusterNotFoundFault{}
					}
					if ref == "" {
						require.Len(t, input.Filters, 1)
						require.Len(t, input.Filters[0].Values, 1)
						assert.Equal(t, "db-cluster-resource-id", aws.ToString(input.Filters[0].Name))
						ref = input.Filters[0].Values[0]
					}
					cluster, ok := clusters[ref]
					require.True(t, ok, "unexpected cluster reference %q", ref)
					return &rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{cluster}}, nil
				})

			resources, err := (&Discoverer{Client: tags, Rds: rdsClient}).Discover(context.Background())

			require.NoError(t, err)
			assert.Equal(t, map[string]model.Resource{
				clusterARN("alpha"): {Type: model.TypeRdsCluster, Ref: "alpha", ARN: clusterARN("alpha"), Tags: map[string]string{model.GroupTagKey: "dev"}},
				clusterARN("omega"): {Type: model.TypeRdsCluster, Ref: "omega", ARN: clusterARN("omega"), Tags: map[string]string{model.GroupTagKey: "other-group"}},
			}, resources)
		})
	}
}

// 同じ resource ID を別パーティション・アカウント・リージョンの候補として返し、範囲をまたいで統合しないことを確認する。
// 参照先と異なる範囲の応答は探索エラーとし、部分結果を返さない。
func TestDiscoverDoesNotReuseClusterIdentityAcrossScopes(t *testing.T) {
	for _, alias := range []string{
		strings.Replace(clusterARN(clusterResourceID), "arn:aws:", "arn:AWS:", 1),
		strings.Replace(clusterARN(clusterResourceID), "123456789012", "999999999999", 1),
		strings.Replace(clusterARN(clusterResourceID), "ap-northeast-1", "us-east-1", 1),
	} {
		t.Run(alias, func(t *testing.T) {
			controller := gomock.NewController(t)
			tags := mocks.NewMockAPI(controller)
			tags.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
				ResourceTagMappingList: []tagtypes.ResourceTagMapping{taggedClusterMapping("alpha"), {ResourceARN: aws.String(alias)}},
			}, nil)
			cluster := testCluster("alpha")
			rdsClient := mocks.NewMockRdsAPI(controller)
			var calls []any
			if clusterARN("alpha") < alias {
				calls = append(calls, rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Eq(&rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterARN("alpha"))})).Return(&rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{cluster}}, nil))
			}
			calls = append(calls,
				rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Eq(&rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(alias)})).Return(nil, &rdstypes.DBClusterNotFoundFault{}),
				rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Eq(&rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(clusterResourceID)})).Return(nil, &rdstypes.DBClusterNotFoundFault{}),
				rdsClient.EXPECT().DescribeDBClusters(gomock.Any(), gomock.Any()).Return(&rds.DescribeDBClustersOutput{DBClusters: []rdstypes.DBCluster{cluster}}, nil),
			)
			gomock.InOrder(calls...)

			resources, err := (&Discoverer{Client: tags, Rds: rdsClient}).Discover(context.Background())

			assert.Nil(t, resources)
			assert.ErrorContains(t, err, alias)
			assert.ErrorContains(t, err, "different partition, region, or account")
		})
	}
}
