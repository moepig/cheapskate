package tagging

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/rds/types"

	"cheapskate/internal/core/model"
)

// クラスタの識別子とタグの取得に用いる、読み取り専用の RDS API。
type RdsAPI interface {
	DescribeDBClusters(ctx context.Context, in *rds.DescribeDBClustersInput, opts ...func(*rds.Options)) (*rds.DescribeDBClustersOutput, error)
}

// RDS クラスタの ARN と Ref を照合し、同一クラスタの検出結果を統合する。
// 該当クラスタまたは所属タグがない候補は除外し、API 失敗と不整合な応答はエラーを返す。
func (d *Discoverer) normalizeClusters(ctx context.Context, resources map[string]model.Resource) (map[string]model.Resource, error) {
	normalized := make(map[string]model.Resource, len(resources))
	var candidates []string
	for key, resource := range resources {
		if resource.Type == model.TypeRdsCluster {
			candidates = append(candidates, key)
		} else {
			normalized[key] = resource
		}
	}
	slices.Sort(candidates)
	if len(candidates) != 0 && d.Rds == nil {
		return nil, fmt.Errorf("discover: RDS client is required to resolve DB clusters")
	}
	cache := map[string]model.Resource{}
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	for _, key := range candidates {
		candidate := resources[key]
		resource, cached := cache[key]
		if !cached {
			cluster, err := d.resolveCluster(ctx, candidate.Ref)
			if err != nil {
				return nil, fmt.Errorf("discover: resolve RDS cluster %q: %w", key, err)
			}
			if cluster == nil {
				log.WarnContext(ctx, "resource-discovery-skipped", "resource_arn", key, "reason", "not-found")
				continue
			}
			resource, err = clusterResource(candidate, *cluster)
			if err != nil {
				return nil, fmt.Errorf("discover: resolve RDS cluster %q: %w", key, err)
			}
			cache[key] = resource
			cache[resource.ARN] = resource
			if id := aws.ToString(cluster.DbClusterResourceId); id != "" {
				alias, _ := arn.Parse(resource.ARN)
				alias.Resource = "cluster:" + id
				cache[alias.String()] = resource
			}
		}
		if _, tagged := resource.Tags[model.GroupTagKey]; !tagged {
			log.WarnContext(ctx, "resource-discovery-skipped", "resource_arn", key, "reason", "membership-tag-missing")
			continue
		}
		normalized[resource.ARN] = resource
	}
	return normalized, nil
}

// クラスタ名を優先して照会し、見つからない resource ID の候補をフィルタで照会する。
func (d *Discoverer) resolveCluster(ctx context.Context, ref string) (*types.DBCluster, error) {
	clusters, err := d.describeClusters(ctx, &rds.DescribeDBClustersInput{DBClusterIdentifier: aws.String(ref)})
	if err != nil {
		if _, notFound := errors.AsType[*types.DBClusterNotFoundFault](err); !notFound {
			return nil, err
		}
	}
	byResourceID := false
	if len(clusters) == 0 && strings.HasPrefix(ref, "cluster-") {
		// https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_DescribeDBClusters.html
		// "db-cluster-resource-id - Accepts DB cluster resource identifiers."
		clusters, err = d.describeClusters(ctx, &rds.DescribeDBClustersInput{Filters: []types.Filter{{
			Name: aws.String("db-cluster-resource-id"), Values: []string{ref},
		}}})
		if err != nil {
			return nil, err
		}
		byResourceID = true
	}
	if len(clusters) == 0 {
		return nil, nil
	}
	if len(clusters) != 1 {
		return nil, fmt.Errorf("DescribeDBClusters returned %d clusters for %q", len(clusters), ref)
	}
	cluster := &clusters[0]
	if byResourceID {
		if aws.ToString(cluster.DbClusterResourceId) != ref {
			return nil, fmt.Errorf("DescribeDBClusters returned a different resource ID for %q", ref)
		}
	} else if !strings.EqualFold(aws.ToString(cluster.DBClusterIdentifier), ref) {
		return nil, fmt.Errorf("DescribeDBClusters returned a different cluster identifier for %q", ref)
	}
	return cluster, nil
}

// 指定した照会条件の全ページを取得する。ページの取得失敗時は部分的な結果を返さない。
func (d *Discoverer) describeClusters(ctx context.Context, input *rds.DescribeDBClustersInput) ([]types.DBCluster, error) {
	var clusters []types.DBCluster
	markers := map[string]bool{}
	for {
		out, err := d.Rds.DescribeDBClusters(ctx, input)
		if err != nil {
			return nil, err
		}
		if out == nil {
			return nil, fmt.Errorf("DescribeDBClusters returned a nil response")
		}
		clusters = append(clusters, out.DBClusters...)
		marker := aws.ToString(out.Marker)
		if marker == "" {
			return clusters, nil
		}
		if markers[marker] {
			return nil, fmt.Errorf("DescribeDBClusters repeated pagination marker %q", marker)
		}
		markers[marker] = true
		input.Marker = out.Marker
	}
}

// RDS 応答のクラスタ名、ARN、およびタグから Resource を組み立てる。
// ARN は入力と同じパーティション・リージョン・アカウントに属し、クラスタ名と一致する必要がある。
func clusterResource(candidate model.Resource, cluster types.DBCluster) (model.Resource, error) {
	resource, err := ParseARN(aws.ToString(cluster.DBClusterArn))
	if err != nil {
		return model.Resource{}, err
	}
	if resource.Type != model.TypeRdsCluster || resource.Ref != aws.ToString(cluster.DBClusterIdentifier) {
		return model.Resource{}, fmt.Errorf("DBClusterArn and DBClusterIdentifier do not match")
	}
	source, _ := arn.Parse(candidate.ARN)
	canonical, _ := arn.Parse(resource.ARN)
	if source.Partition != canonical.Partition || source.Region != canonical.Region || source.AccountID != canonical.AccountID {
		return model.Resource{}, fmt.Errorf("DBClusterArn %q belongs to a different partition, region, or account", resource.ARN)
	}
	resource.Tags = map[string]string{}
	for _, tag := range cluster.TagList {
		if tag.Key != nil {
			resource.Tags[*tag.Key] = aws.ToString(tag.Value)
		}
	}
	return resource, nil
}
