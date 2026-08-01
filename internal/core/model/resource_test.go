package model

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 種別の宣言が破れていると、その種別は探索・検証・表示から静かに抜け落ちる
// 実行時に気づく経路がないので（不正な ARN が「未対応」として返るだけなので）ここで縛る
func TestTypeInfoDeclarations(t *testing.T) {
	byType := map[ResourceType]bool{}
	byARN := map[string]ResourceType{}
	for _, i := range typeInfos {
		assert.NotEmptyf(t, i.ARNService, "%s: ARN service is required", i.Type)
		assert.NotEmptyf(t, i.ARNResource, "%s: ARN resource type is required", i.Type)
		// RefPattern がないと Resource.Validate が nil 参照で落ちる
		require.NotNilf(t, i.RefPattern, "%s: ref pattern is required", i.Type)

		assert.Falsef(t, byType[i.Type], "%s is declared twice", i.Type)
		byType[i.Type] = true

		// (service, resource-type) が重なると、その ARN からどちらの種別を引くかが宣言順に依存する
		key := i.TaggingFilter()
		prev, dup := byARN[key]
		assert.Falsef(t, dup, "%s and %s both claim ARN %s", prev, i.Type, key)
		byARN[key] = i.Type

		names := map[string]bool{}
		for _, c := range i.ConfigTags {
			assert.NotEmptyf(t, c.Key, "%s: config tag needs a tag key", i.Type)
			assert.NotEmptyf(t, c.Label, "%s: config tag %q needs a label", i.Type, c.Name)
			assert.Falsef(t, names[c.Name], "%s: config name %q is used twice", i.Type, c.Name)
			names[c.Name] = true
		}
	}

	// KnownTypes は宣言から導く（Selector.Types の正規化がソート順に依っている）
	assert.Equal(t, len(typeInfos), len(KnownTypes))
	assert.True(t, slices.IsSorted(KnownTypes), "KnownTypes must be sorted")
	for _, typ := range KnownTypes {
		info, ok := Info(typ)
		require.Truef(t, ok, "%s is in KnownTypes but has no TypeInfo", typ)
		assert.Equal(t, info, mustInfoByARN(t, info.ARNService, info.ARNResource), "Info and InfoByARN must agree")
	}

	_, ok := Info("sqs-queue")
	assert.False(t, ok, "unknown types must not resolve")
	_, ok = InfoByARN("sqs", "queue")
	assert.False(t, ok, "unmanaged ARNs must not resolve")
}

func mustInfoByARN(t *testing.T, service, resource string) TypeInfo {
	t.Helper()
	info, ok := InfoByARN(service, resource)
	require.Truef(t, ok, "no type declared for ARN %s:%s", service, resource)
	return info
}

func TestResourceID(t *testing.T) {
	cases := []struct {
		r    Resource
		want string
	}{
		{Resource{Type: TypeRdsInstance, Ref: "dev-db"}, "rds-instance#dev-db"},
		{Resource{Type: TypeRdsCluster, Ref: "dev-aurora"}, "rds-cluster#dev-aurora"},
		{Resource{Type: TypeEcsService, Ref: "dev-cluster/api"}, "ecs-service#dev-cluster/api"},
		{Resource{Type: TypeEc2Instance, Ref: "i-0abc123"}, "ec2-instance#i-0abc123"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, c.r.ID())
	}
}

// 種別と Ref の噛み合いは探索の時点で確かめる
// stop/start を試みる瞬間ではなく、リソースが見つかった時点で形式の誤りが分かるようにするためである
func TestResourceValidate(t *testing.T) {
	ok := []Resource{
		{Type: TypeRdsInstance, Ref: "db1"},
		{Type: TypeRdsCluster, Ref: "dev-cluster"},
		{Type: TypeEcsService, Ref: "dev-cluster/api"},
		{Type: TypeEc2Instance, Ref: "i-0abc123"},
	}
	for _, r := range ok {
		assert.NoErrorf(t, r.Validate(), "%s#%s should be valid", r.Type, r.Ref)
	}

	bad := []Resource{
		{Type: "sqs-queue", Ref: "q"},               // 未知の種別
		{Type: TypeRdsInstance, Ref: ""},            // ref なし
		{Type: TypeEcsService, Ref: "api"},          // cluster がない
		{Type: TypeEcsService, Ref: "/api"},         // cluster が空
		{Type: TypeEcsService, Ref: "dev-cluster/"}, // service が空
		{Type: TypeEc2Instance, Ref: "db1"},         // インスタンス ID でない
	}
	for _, r := range bad {
		assert.Errorf(t, r.Validate(), "%s#%s should be rejected", r.Type, r.Ref)
	}
}

// 設定として見せるのは、その種別が意味を与えているタグだけである
// リソースに付いた全タグを流すと、無関係な運用タグ（Name、コスト配賦など）まで cheapskate の設定として読めてしまう
func TestResourceConfigReadsDeclaredTagsOnly(t *testing.T) {
	r := Resource{
		Type: TypeEcsService,
		Tags: map[string]string{
			EcsDesiredCountTagKey: "2",
			EcsScalingMinTagKey:   "1",
			EcsScalingMaxTagKey:   "3",
			"unrelated":           "tag",
		},
	}
	assert.Equal(t, []ConfigValue{
		{Name: "desired_count", Label: "desired", Value: "2"},
		{Name: "min", Label: "scaling min", Value: "1"},
		{Name: "max", Label: "scaling max", Value: "3"},
	}, r.Config(), "宣言の順に、宣言されたタグだけを返す")

	assert.Empty(t, Resource{Type: TypeEcsService}.Config(), "no scaling tags set")

	// スケーリングタグが付いていても、ecs-service 以外はそれを設定として扱わない
	other := Resource{Type: TypeRdsInstance, Tags: r.Tags}
	assert.Empty(t, other.Config())

	assert.Empty(t, Resource{Type: "sqs-queue", Tags: r.Tags}.Config(), "unknown type")
}
