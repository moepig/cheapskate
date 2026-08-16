// Resource Groups Tagging API を通じて、ターゲットグループのセレクタに一致する AWS リソースを探索する
// この API を呼ぶのは本パッケージに限る。port.Discoverer を実装する
package tagging

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"

	"cheapskate/internal/core/model"
)

//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks cheapskate/internal/aws/tagging API

// 利用する Resource Groups Tagging API クライアントの部分集合
type API interface {
	GetResources(ctx context.Context, in *resourcegroupstaggingapi.GetResourcesInput, opts ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error)
}

// Resource Groups Tagging API を用いて port.Discoverer を実装する
type Discoverer struct {
	Client API
}

// sel.TagKey=sel.TagValue が付与され、種別が sel.Types に含まれるリソースをすべて返す
// reconcile の順序と表示を決定的とするため、Resource.ID() でソートする
// 解釈できない ARN が存在する場合、呼び出し全体を失敗させる
// 該当するのは旧形式の ARN (ParseARN を参照)、またはフィルタと対応づけの不具合であり、スキップした場合はこれらの不備が検知されないためである
func (d *Discoverer) Discover(ctx context.Context, sel model.Selector) ([]model.Resource, error) {
	typeFilters := make([]string, 0, len(sel.Types))
	for _, t := range sel.Types {
		info, ok := model.Info(t)
		if !ok {
			return nil, fmt.Errorf("discover: unknown resource type %q", t)
		}
		typeFilters = append(typeFilters, info.TaggingFilter())
	}

	var resources []model.Resource
	var token *string
	for {
		out, err := d.Client.GetResources(ctx, &resourcegroupstaggingapi.GetResourcesInput{
			TagFilters:          []types.TagFilter{{Key: &sel.TagKey, Values: []string{sel.TagValue}}},
			ResourceTypeFilters: typeFilters,
			PaginationToken:     token,
		})
		if err != nil {
			return nil, fmt.Errorf("discover: GetResources: %w", err)
		}
		for _, m := range out.ResourceTagMappingList {
			if m.ResourceARN == nil {
				continue
			}
			// ParseARN は種別ごとの Ref 形式まで検証する
			// これを通過した Ref に対し、以降のターゲット操作は形式の検証を行わない
			r, err := ParseARN(*m.ResourceARN)
			if err != nil {
				return nil, fmt.Errorf("discover: %w", err)
			}
			r.Tags = tagsToMap(m.Tags)
			resources = append(resources, r)
		}
		if out.PaginationToken == nil || *out.PaginationToken == "" {
			break
		}
		token = out.PaginationToken
	}

	sort.Slice(resources, func(i, j int) bool { return resources[i].ID() < resources[j].ID() })
	return resources, nil
}

// Tagging API のキーと値の組をマップへ平坦化し、種別固有の設定の参照に用いる
// Key が nil の組はスキップする
// 壊れたタグ 1 件により探索全体を失敗させないためである
func tagsToMap(tags []types.Tag) map[string]string {
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		if t.Key == nil {
			continue
		}
		v := ""
		if t.Value != nil {
			v = *t.Value
		}
		m[*t.Key] = v
	}
	return m
}

// リソース ARN を model.Resource へ対応づける
// ARN の末尾(リソース部)は "<resource-type><区切り><ref>" の形をしており、その (service, resource-type) の組から model.InfoByARN が種別を引く:
//
//	arn:aws:rds:region:account:db:NAME              -> {rds-instance, NAME}
//	arn:aws:rds:region:account:cluster:NAME         -> {rds-cluster,  NAME}
//	arn:aws:ecs:region:account:service/CLUSTER/NAME -> {ecs-service,  CLUSTER/NAME}
//	arn:aws:ec2:region:account:instance/i-...       -> {ec2-instance, i-...}
//
// 区切りが ":" と "/" のいずれであるかは AWS のサービスごとに異なり、種別の判別には用いない
// したがって宣言には持たせず、先に現れた文字を区切りとして扱う
// 残りの全体が Ref となるため、ECS の "CLUSTER/NAME" のように区切りを含む Ref も通る
//
// 種別が確定した時点で Ref の形式も検証する (model.Resource.Validate を参照)
// 探索が Resource を組み立てた時点で拒否しない場合、形式の誤りは stop/start の実行時まで現れない
// 対象の ARN を必ずエラーへ含める
// ARN を含めない場合、修正すべきリソースを特定できないためである
func ParseARN(arnStr string) (model.Resource, error) {
	parts := strings.SplitN(arnStr, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" {
		return model.Resource{}, fmt.Errorf("malformed ARN: %q", arnStr)
	}
	service, resource := parts[2], parts[5]

	sep := strings.IndexAny(resource, ":/")
	if sep < 0 {
		return model.Resource{}, fmt.Errorf("ARN %q has no resource type in its resource part %q", arnStr, resource)
	}
	resourceType, ref := resource[:sep], resource[sep+1:]

	info, ok := model.InfoByARN(service, resourceType)
	if !ok {
		return model.Resource{}, fmt.Errorf("unsupported resource %q of service %q in ARN %q", resourceType, service, arnStr)
	}
	r := model.Resource{Type: info.Type, Ref: ref, ARN: arnStr}
	if err := r.Validate(); err != nil {
		return model.Resource{}, fmt.Errorf("ARN %q: %w", arnStr, err)
	}
	return r, nil
}
