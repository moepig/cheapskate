// `make dev` 向けに、ローカルのエミュレータ（Floci）へダミーの ECS リソースを作る
// これにより web console と CLI が空のリソース一覧ではなく、実在して多様なデータを表示できる
// 冪等なので再実行しても安全であり、cmd/dev-bootstrap と同じく Lambda のコンテナイメージには決して含めない
package devseed

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"

	"cheapskate/internal/core/model"
)

const (
	cluster = "dev-cluster"
	family  = "dev-task"
)

//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks cheapskate/internal/devtools/devseed EcsAPI,TaggingAPI

// Seed が使う ECS クライアントの部分集合
type EcsAPI interface {
	CreateCluster(ctx context.Context, in *ecs.CreateClusterInput, opts ...func(*ecs.Options)) (*ecs.CreateClusterOutput, error)
	DescribeTaskDefinition(ctx context.Context, in *ecs.DescribeTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error)
	RegisterTaskDefinition(ctx context.Context, in *ecs.RegisterTaskDefinitionInput, opts ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error)
	DescribeServices(ctx context.Context, in *ecs.DescribeServicesInput, opts ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	CreateService(ctx context.Context, in *ecs.CreateServiceInput, opts ...func(*ecs.Options)) (*ecs.CreateServiceOutput, error)
}

// Seed が使う Resource Groups Tagging API クライアントの部分集合
type TaggingAPI interface {
	TagResources(ctx context.Context, in *resourcegroupstaggingapi.TagResourcesInput, opts ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.TagResourcesOutput, error)
}

// 投入するダミーの ECS サービス 1 件と、それに付けるべきタグ
// タグは ECS 自身の --tags ではなく Resource Groups Tagging API 経由で適用する
// Floci は作成時のサービスタグを tag:GetResources に反映しないためである（docs/en/development/run_local.md を参照）
// そうしなければ tagging.Discoverer からは決して見えない
type ecsService struct {
	name string
	tags map[string]string
}

// グループ所属の両側と、web console や CLI のリソース表示における ECS スケーリングタグの両方を通すため、あえて混在させている:
//   - "api" と "worker" は cheapskate:group=dev を持ち、サンプルの "dev" グループのセレクタ（scripts/dev.sh）にマッチする
//     "worker" はさらに ECS スケーリングタグ一式を持つ
//     （model.EcsDesiredCountTagKey・EcsScalingMinTagKey・EcsScalingMaxTagKey）
//   - "batch" はそのタグを持たないので、Floci には現れるが "dev" グループの Resources には出ない
var services = []ecsService{
	{name: "api", tags: map[string]string{
		"cheapskate:group":          "dev",
		model.EcsDesiredCountTagKey: "2",
	}},
	{name: "worker", tags: map[string]string{
		"cheapskate:group":          "dev",
		model.EcsDesiredCountTagKey: "1",
		model.EcsScalingMinTagKey:   "1",
		model.EcsScalingMaxTagKey:   "3",
	}},
	{name: "batch", tags: map[string]string{
		"env": "dev",
	}},
}

// dev クラスタ、ダミーのタスク定義 1 つ、`services` に挙げたサービス群を作り、それぞれにタグを付ける
// すでに存在するもの（クラスタ、タスク定義、個々のサービス）はそのまま残す
// タグだけは常に付け直す
// Floci のタグ保管は、そこで describe されるリソースと違って `docker compose down`/`up` をまたいで残らないためである
func Seed(ctx context.Context, ecsClient EcsAPI, taggingClient TaggingAPI) error {
	if _, err := ecsClient.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)}); err != nil {
		return fmt.Errorf("devseed: create cluster %s: %w", cluster, err)
	}

	taskDefArn, err := ensureTaskDefinition(ctx, ecsClient)
	if err != nil {
		return err
	}

	for _, svc := range services {
		arn, err := ensureService(ctx, ecsClient, taskDefArn, svc.name)
		if err != nil {
			return err
		}
		if len(svc.tags) == 0 {
			continue
		}
		if _, err := taggingClient.TagResources(ctx, &resourcegroupstaggingapi.TagResourcesInput{
			ResourceARNList: []string{arn},
			Tags:            svc.tags,
		}); err != nil {
			return fmt.Errorf("devseed: tag service %s: %w", svc.name, err)
		}
	}
	return nil
}

// family の active なタスク定義の ARN を返す
// family がまだ存在しなければ、ダミーのタスク定義を登録する
// ダミーは nginx コンテナ 1 つだけで、実際には動かさないのでイメージが取得されることもない
func ensureTaskDefinition(ctx context.Context, c EcsAPI) (string, error) {
	out, err := c.DescribeTaskDefinition(ctx, &ecs.DescribeTaskDefinitionInput{TaskDefinition: aws.String(family)})
	if err == nil {
		return *out.TaskDefinition.TaskDefinitionArn, nil
	}
	if _, ok := errors.AsType[*ecstypes.ClientException](err); !ok {
		return "", fmt.Errorf("devseed: describe task definition %s: %w", family, err)
	}

	reg, err := c.RegisterTaskDefinition(ctx, &ecs.RegisterTaskDefinitionInput{
		Family:                  aws.String(family),
		RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
		NetworkMode:             ecstypes.NetworkModeAwsvpc,
		Cpu:                     aws.String("256"),
		Memory:                  aws.String("512"),
		ContainerDefinitions: []ecstypes.ContainerDefinition{{
			Name:      aws.String("app"),
			Image:     aws.String("nginx:latest"),
			Essential: aws.Bool(true),
		}},
	})
	if err != nil {
		return "", fmt.Errorf("devseed: register task definition %s: %w", family, err)
	}
	return *reg.TaskDefinition.TaskDefinitionArn, nil
}

// その名前の既存の ACTIVE なサービスの ARN を返し、なければ作成する
// 作成時は desired count 1、ダミーのサブネット上とし、Floci に実際の VPC はないので実際にスケジュールされることはない
func ensureService(ctx context.Context, c EcsAPI, taskDefArn, name string) (string, error) {
	desc, err := c.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(cluster), Services: []string{name}})
	if err != nil {
		return "", fmt.Errorf("devseed: describe service %s: %w", name, err)
	}
	for _, s := range desc.Services {
		if s.Status != nil && *s.Status == "ACTIVE" {
			return *s.ServiceArn, nil
		}
	}

	out, err := c.CreateService(ctx, &ecs.CreateServiceInput{
		Cluster:        aws.String(cluster),
		ServiceName:    aws.String(name),
		TaskDefinition: aws.String(taskDefArn),
		DesiredCount:   aws.Int32(1),
		LaunchType:     ecstypes.LaunchTypeFargate,
		NetworkConfiguration: &ecstypes.NetworkConfiguration{
			AwsvpcConfiguration: &ecstypes.AwsVpcConfiguration{
				Subnets:        []string{"subnet-00000000"},
				AssignPublicIp: ecstypes.AssignPublicIpEnabled,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("devseed: create service %s: %w", name, err)
	}
	return *out.Service.ServiceArn, nil
}
