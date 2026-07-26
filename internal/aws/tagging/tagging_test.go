package tagging

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/aws/tagging/mocks"
	"cheapskate/internal/core/model"
)

func TestParseARN(t *testing.T) {
	cases := []struct {
		arn  string
		want model.Resource
	}{
		{"arn:aws:rds:ap-northeast-1:123456789012:db:dev-db", model.Resource{Type: model.TypeRdsInstance, Ref: "dev-db", ARN: "arn:aws:rds:ap-northeast-1:123456789012:db:dev-db"}},
		{"arn:aws:rds:ap-northeast-1:123456789012:cluster:dev-aurora", model.Resource{Type: model.TypeRdsCluster, Ref: "dev-aurora", ARN: "arn:aws:rds:ap-northeast-1:123456789012:cluster:dev-aurora"}},
		{"arn:aws:ecs:ap-northeast-1:123456789012:service/dev-cluster/api", model.Resource{Type: model.TypeEcsService, Ref: "dev-cluster/api", ARN: "arn:aws:ecs:ap-northeast-1:123456789012:service/dev-cluster/api"}},
		{"arn:aws:ec2:ap-northeast-1:123456789012:instance/i-0abc123", model.Resource{Type: model.TypeEc2Instance, Ref: "i-0abc123", ARN: "arn:aws:ec2:ap-northeast-1:123456789012:instance/i-0abc123"}},
	}
	for _, tc := range cases {
		got, err := ParseARN(tc.arn)
		require.NoError(t, err, tc.arn)
		assert.Equal(t, tc.want, got, tc.arn)
	}
}

func TestParseARNRejects(t *testing.T) {
	cases := []struct {
		name string
		arn  string
	}{
		{"not an arn", "not-an-arn"},
		{"too few segments", "arn:aws:rds:ap-northeast-1:123456789012"},
		{"unsupported service", "arn:aws:sqs:ap-northeast-1:123456789012:queue:dev"},
		{"unknown rds resource type", "arn:aws:rds:ap-northeast-1:123456789012:snapshot:dev"},
		// RDS の ARN は末尾が "<type>:<name>" でなければならない
		// 区切りの ":" がないと、名前を種別として読んでしまい黙って別のリソースを掴みかねない
		{"rds arn without a resource type", "arn:aws:rds:ap-northeast-1:123456789012:dev-db"},
		{"unrecognized ecs resource", "arn:aws:ecs:ap-northeast-1:123456789012:cluster/dev-cluster"},
		{"unrecognized ec2 resource", "arn:aws:ec2:ap-northeast-1:123456789012:volume/vol-0abc"},
		// 短形式の ECS サービス ARN（アカウント設定が長い ARN 形式へ移行していない場合）はクラスタ名を含まない
		// そのため cheapskate の "<cluster>/<service>" という ref の形へ対応づけられない
		// リソースを黙って落とすのではなく、対処方法を示して明確に失敗しなければならない
		{"short-form ecs arn", "arn:aws:ecs:ap-northeast-1:123456789012:service/api"},
	}
	for _, tc := range cases {
		_, err := ParseARN(tc.arn)
		assert.Errorf(t, err, "%s: %s", tc.name, tc.arn)
	}
}

func TestDiscoverFiltersAndSorts(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockAPI(ctrl)
	c.EXPECT().GetResources(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, in *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
			require.Len(t, in.TagFilters, 1)
			assert.Equal(t, "env", *in.TagFilters[0].Key)
			assert.Equal(t, []string{"dev"}, in.TagFilters[0].Values)
			assert.ElementsMatch(t, []string{"rds:db", "ecs:service"}, in.ResourceTypeFilters)
			return &resourcegroupstaggingapi.GetResourcesOutput{
				ResourceTagMappingList: []types.ResourceTagMapping{
					{ResourceARN: new("arn:aws:ecs:ap-northeast-1:123456789012:service/dev-cluster/api")},
					{ResourceARN: new("arn:aws:rds:ap-northeast-1:123456789012:db:aaa-db")},
				},
			}, nil
		})
	d := &Discoverer{Client: c}

	got, err := d.Discover(context.Background(), model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance, model.TypeEcsService}})
	require.NoError(t, err)
	require.Len(t, got, 2)
	// Resource.ID() でソートされる
	// 辞書順で "ecs-service..." < "rds-instance..." となる（e < r）ので、ecs が先に来る
	assert.Equal(t, "ecs-service#dev-cluster/api", got[0].ID())
	assert.Equal(t, "rds-instance#aaa-db", got[1].ID())
}

// セレクタ自身の tag_key/tag_value だけでなく、リソースに付いた全タグが通らなければならない
// EcsServiceTarget.Start は、まさにこのマップから model.EcsDesiredCountTagKey を読む
func TestDiscoverCarriesResourceTags(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockAPI(ctrl)
	c.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
		ResourceTagMappingList: []types.ResourceTagMapping{{
			ResourceARN: new("arn:aws:ecs:ap-northeast-1:123456789012:service/dev-cluster/api"),
			Tags: []types.Tag{
				{Key: new("env"), Value: new("dev")},
				{Key: new(model.EcsDesiredCountTagKey), Value: new("3")},
			},
		}},
	}, nil)
	d := &Discoverer{Client: c}

	got, err := d.Discover(context.Background(), model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeEcsService}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, map[string]string{"env": "dev", model.EcsDesiredCountTagKey: "3"}, got[0].Tags)
}

// Tagging API のレスポンスはどのフィールドもポインタなので、nil で返ってくる余地がある
// ARN が nil なら、そもそもどのリソースか特定できないのだから飛ばすしかない
// Key が nil のタグも同じく飛ばす
// いずれも壊れた 1 件で探索全体を落とさず、読めたリソースは通常どおり返す
func TestDiscoverSkipsNilARNsAndNilTagKeys(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockAPI(ctrl)
	c.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
		ResourceTagMappingList: []types.ResourceTagMapping{
			{ResourceARN: nil, Tags: []types.Tag{{Key: new("env"), Value: new("dev")}}},
			{
				ResourceARN: new("arn:aws:rds:ap-northeast-1:123456789012:db:dev-db"),
				Tags: []types.Tag{
					{Key: nil, Value: new("orphaned")},
					{Key: new("env"), Value: new("dev")},
					{Key: new("no-value"), Value: nil}, // 値が nil なら空文字として持つ
				},
			},
		},
	}, nil)
	d := &Discoverer{Client: c}

	got, err := d.Discover(context.Background(), model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}})

	require.NoError(t, err, "壊れた 1 件で探索全体を落としてはならない")
	require.Len(t, got, 1, "ARN の分からないリソースは返しようがない")
	assert.Equal(t, "rds-instance#dev-db", got[0].ID())
	assert.Equal(t, map[string]string{"env": "dev", "no-value": ""}, got[0].Tags)
}

func TestDiscoverPages(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockAPI(ctrl)
	token := "page2"
	gomock.InOrder(
		c.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
			ResourceTagMappingList: []types.ResourceTagMapping{{ResourceARN: new("arn:aws:rds:ap-northeast-1:123456789012:db:a")}},
			PaginationToken:        &token,
		}, nil),
		c.EXPECT().GetResources(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, in *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
				require.NotNil(t, in.PaginationToken)
				assert.Equal(t, token, *in.PaginationToken)
				return &resourcegroupstaggingapi.GetResourcesOutput{
					ResourceTagMappingList: []types.ResourceTagMapping{{ResourceARN: new("arn:aws:rds:ap-northeast-1:123456789012:db:b")}},
				}, nil
			}),
	)
	d := &Discoverer{Client: c}

	got, err := d.Discover(context.Background(), model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "rds-instance#a", got[0].ID())
	assert.Equal(t, "rds-instance#b", got[1].ID())
}

func TestDiscoverGetResourcesErrorPropagates(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockAPI(ctrl)
	wantErr := errors.New("access denied")
	c.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(nil, wantErr)
	d := &Discoverer{Client: c}

	_, err := d.Discover(context.Background(), model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}})
	require.ErrorIs(t, err, wantErr)
}

func TestDiscoverMalformedARNFailsWholeCall(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockAPI(ctrl)
	c.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
		ResourceTagMappingList: []types.ResourceTagMapping{{ResourceARN: new("not-an-arn")}},
	}, nil)
	d := &Discoverer{Client: c}

	_, err := d.Discover(context.Background(), model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}})
	assert.Error(t, err, "a malformed ARN must fail the whole Discover call, not be silently skipped")
}

// 種別と噛み合わない Ref も探索の時点で弾く
// ARN の形としては通ってしまう値（instance/ なのにインスタンス ID でない、など）がそのまま流れると、誤りが分かるのは stop/start を試みる瞬間まで遅れる
func TestDiscoverRejectsRefThatDoesNotMatchItsType(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockAPI(ctrl)
	arn := "arn:aws:ec2:ap-northeast-1:123456789012:instance/not-an-instance-id"
	c.EXPECT().GetResources(gomock.Any(), gomock.Any()).Return(&resourcegroupstaggingapi.GetResourcesOutput{
		ResourceTagMappingList: []types.ResourceTagMapping{{ResourceARN: new(arn)}},
	}, nil)
	d := &Discoverer{Client: c}

	_, err := d.Discover(context.Background(), model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeEc2Instance}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), arn, "どの ARN が問題なのかを伝えなければ直せない")
}

func TestDiscoverUnknownTypeFilter(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockAPI(ctrl)
	// GetResources に EXPECT はない
	// 対応づけのない種別は API 呼び出しの前に失敗しなければならないためである
	d := &Discoverer{Client: c}

	_, err := d.Discover(context.Background(), model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{"unknown-type"}})
	assert.Error(t, err)
}
