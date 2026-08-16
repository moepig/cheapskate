package devseed

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"

	"cheapskate/internal/devtools/devseed/mocks"
)

func TestEnsureTaskDefinitionReturnsExistingWhenPresent(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEcsAPI(ctrl)
	c.EXPECT().DescribeTaskDefinition(gomock.Any(), gomock.Any()).Return(&ecs.DescribeTaskDefinitionOutput{
		TaskDefinition: &ecstypes.TaskDefinition{TaskDefinitionArn: aws.String("arn:existing")},
	}, nil)
	// RegisterTaskDefinition の EXPECT は設定しない
	// 想定外の呼び出しは、モックコントローラによりテストの失敗となる

	arn, err := ensureTaskDefinition(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, "arn:existing", arn)
}

func TestEnsureTaskDefinitionRegistersWhenMissing(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEcsAPI(ctrl)
	c.EXPECT().DescribeTaskDefinition(gomock.Any(), gomock.Any()).
		Return(nil, &ecstypes.ClientException{Message: aws.String("no such family")})
	c.EXPECT().RegisterTaskDefinition(gomock.Any(), gomock.Any()).Return(&ecs.RegisterTaskDefinitionOutput{
		TaskDefinition: &ecstypes.TaskDefinition{TaskDefinitionArn: aws.String("arn:new")},
	}, nil)

	arn, err := ensureTaskDefinition(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, "arn:new", arn)
}

func TestEnsureTaskDefinitionPropagatesUnexpectedDescribeError(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEcsAPI(ctrl)
	c.EXPECT().DescribeTaskDefinition(gomock.Any(), gomock.Any()).Return(nil, errors.New("network down"))
	// RegisterTaskDefinition の EXPECT は設定しない
	// ClientException 以外の describe エラーは、register へ到達してはならない

	_, err := ensureTaskDefinition(context.Background(), c)
	assert.ErrorContains(t, err, "network down")
}

func TestEnsureServiceReturnsExistingActiveService(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEcsAPI(ctrl)
	c.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{
		Services: []ecstypes.Service{{Status: aws.String("ACTIVE"), ServiceArn: aws.String("arn:svc-api")}},
	}, nil)
	// CreateService の EXPECT は設定しない
	// 既存の ACTIVE なサービスを再作成してはならない

	arn, err := ensureService(context.Background(), c, "arn:taskdef", "api")
	require.NoError(t, err)
	assert.Equal(t, "arn:svc-api", arn)
}

func TestEnsureServiceIgnoresNonActiveServiceAndCreates(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEcsAPI(ctrl)
	c.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(&ecs.DescribeServicesOutput{
		Services: []ecstypes.Service{{Status: aws.String("INACTIVE"), ServiceArn: aws.String("arn:old")}},
	}, nil)
	c.EXPECT().CreateService(gomock.Any(), gomock.Any()).Return(&ecs.CreateServiceOutput{
		Service: &ecstypes.Service{ServiceArn: aws.String("arn:svc-new")},
	}, nil)

	arn, err := ensureService(context.Background(), c, "arn:taskdef", "api")
	require.NoError(t, err)
	assert.Equal(t, "arn:svc-new", arn)
}

func TestEnsureServicePropagatesDescribeError(t *testing.T) {
	ctrl := gomock.NewController(t)
	c := mocks.NewMockEcsAPI(ctrl)
	c.EXPECT().DescribeServices(gomock.Any(), gomock.Any()).Return(nil, errors.New("boom"))

	_, err := ensureService(context.Background(), c, "arn:taskdef", "api")
	assert.ErrorContains(t, err, "boom")
}

// Seed はクラスタを作成し、全サービス共通のタスク定義 1 つを再利用または登録しなければならない
// 新規作成と既存のいずれのサービスに対しても、Resource Groups Tagging API を通じてタグを付与する
// "worker" の ECS スケーリングタグも対象に含む (この構成の根拠は `services` 変数のコメントを参照)
func TestSeedCreatesClusterTaskDefinitionAndTagsEveryService(t *testing.T) {
	ctrl := gomock.NewController(t)
	ecsAPI := mocks.NewMockEcsAPI(ctrl)
	taggingAPI := mocks.NewMockTaggingAPI(ctrl)

	ecsAPI.EXPECT().CreateCluster(gomock.Any(), gomock.Any()).Return(&ecs.CreateClusterOutput{}, nil)
	ecsAPI.EXPECT().DescribeTaskDefinition(gomock.Any(), gomock.Any()).Return(&ecs.DescribeTaskDefinitionOutput{
		TaskDefinition: &ecstypes.TaskDefinition{TaskDefinitionArn: aws.String("arn:taskdef")},
	}, nil)

	taggedARNs := map[string][]string{}
	for _, name := range []string{"api", "worker", "batch"} {
		arn := "arn:svc-" + name
		ecsAPI.EXPECT().DescribeServices(gomock.Any(), matchesService(name)).Return(&ecs.DescribeServicesOutput{
			Services: []ecstypes.Service{{Status: aws.String("ACTIVE"), ServiceArn: aws.String(arn)}},
		}, nil)
		taggingAPI.EXPECT().TagResources(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, in *resourcegroupstaggingapi.TagResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.TagResourcesOutput, error) {
				for _, a := range in.ResourceARNList {
					for k := range in.Tags {
						taggedARNs[a] = append(taggedARNs[a], k)
					}
				}
				return &resourcegroupstaggingapi.TagResourcesOutput{}, nil
			})
	}

	require.NoError(t, Seed(context.Background(), ecsAPI, taggingAPI))

	assert.ElementsMatch(t, []string{"cheapskate:group", "cheapskate/desired-count"}, taggedARNs["arn:svc-api"])
	assert.ElementsMatch(t, []string{"cheapskate:group", "cheapskate/desired-count", "cheapskate/scaling-min", "cheapskate/scaling-max"}, taggedARNs["arn:svc-worker"])
	assert.ElementsMatch(t, []string{"env"}, taggedARNs["arn:svc-batch"])
}

func TestSeedPropagatesCreateClusterError(t *testing.T) {
	ctrl := gomock.NewController(t)
	ecsAPI := mocks.NewMockEcsAPI(ctrl)
	taggingAPI := mocks.NewMockTaggingAPI(ctrl)
	ecsAPI.EXPECT().CreateCluster(gomock.Any(), gomock.Any()).Return(nil, errors.New("aws down"))
	// これ以上の EXPECT は設定しない
	// クラスタの作成の失敗は、タスク定義とサービスの操作の前に Seed を中断させなければならない

	err := Seed(context.Background(), ecsAPI, taggingAPI)
	assert.ErrorContains(t, err, "aws down")
}

func matchesService(name string) gomock.Matcher {
	return gomock.WantFormatter(gomock.StringerFunc(func() string { return "service " + name }),
		gomock.Cond(func(x any) bool {
			in, ok := x.(*ecs.DescribeServicesInput)
			return ok && len(in.Services) == 1 && in.Services[0] == name
		}))
}
