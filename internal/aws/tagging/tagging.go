// Resource Groups Tagging API を通じて、ターゲットグループのセレクタにマッチする AWS リソースを見つける
// この API に触れるのはこのパッケージだけであり、port.Discoverer を実装する
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

// sel.TagKey=sel.TagValue が付き、種別が sel.Types に含まれるリソースをすべて返す
// reconcile の順序と表示を決定的にするため、Resource.ID() でソートする
// 解釈できない ARN があれば呼び出し全体を失敗させる
// それは旧形式の ARN（ParseARN を参照）か、フィルタや対応づけの不具合を示しており、黙って飛ばせば本物の設定不備を覆い隠すためである
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
			// ParseARN は種別ごとの Ref 形式まで確かめて返す
			// 通れば、以降のターゲット操作は形式について心配しなくてよい
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

// Tagging API のキー/値の組をマップへ平坦化し、ターゲット固有の参照に使えるようにする
// EcsServiceTarget が model.EcsDesiredCountTagKey を読む場合などがこれにあたる
// Key が nil のものは飛ばす
// 壊れたタグ 1 つで探索全体を失敗させないためである
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
// ARN の末尾（リソース部）は "<resource-type><区切り><ref>" の形をしており、その (service, resource-type) の組から model.InfoByARN が種別を引く:
//
//	arn:aws:rds:region:account:db:NAME              -> {rds-instance, NAME}
//	arn:aws:rds:region:account:cluster:NAME         -> {rds-cluster,  NAME}
//	arn:aws:ecs:region:account:service/CLUSTER/NAME -> {ecs-service,  CLUSTER/NAME}
//	arn:aws:ec2:region:account:instance/i-...       -> {ec2-instance, i-...}
//
// 区切りが ":" か "/" かは AWS のサービスによって違うだけで、どちらであるかに意味はない
// そのため宣言には持たせず、ここで先に現れた方を区切りとして扱う
// 残り全体が Ref になるので、ECS の "CLUSTER/NAME" のように区切りを含む Ref もそのまま通る
//
// 種別が分かった時点で Ref の形式も確かめる（model.Resource.Validate を参照）
// 探索が Resource を組み立てた瞬間に弾いておかないと、形式の誤りは stop/start まで遅れて現れる
// どの ARN が問題なのかを必ずエラーに含める
// ARN を挙げずに「ref が不正」とだけ言われても、どのリソースのタグを直せばよいか分からない
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
