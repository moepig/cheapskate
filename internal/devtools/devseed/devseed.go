// `make dev` 向けに、ローカルのエミュレータ (Floci) へダミーの ECS リソースを作成する
// これにより、web console と CLI は空でないリソース一覧を表示する
// 冪等であり再実行に対応する。cmd/dev-bootstrap と同じく、Lambda のコンテナイメージには含めない
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

// 投入するダミーの ECS サービス 1 件と、それへ付与するタグ
// タグは ECS の --tags ではなく、Resource Groups Tagging API を通じて適用する
// Floci は作成時のサービスタグを tag:GetResources へ反映しないためである (docs/en/development/run_local.md を参照)
// 適用しない場合、tagging.Discoverer から探索できない
type ecsService struct {
	name string
	tags map[string]string
}

// グループ所属の有無と、web console および CLI における ECS スケーリングタグの表示の双方を検証するため、次の構成とする:
//   - "api" と "worker" は cheapskate:group=dev を持ち、サンプルの "dev" グループのセレクタ (scripts/dev.sh) に一致する
//     "worker" はさらに ECS スケーリングタグ一式を持つ
//     (model.EcsDesiredCountTagKey、EcsScalingMinTagKey、EcsScalingMaxTagKey)
//   - "batch" はそのタグを持たないため、Floci には存在するが "dev" グループの Resources には現れない
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

// dev クラスタ、ダミーのタスク定義 1 つ、`services` に定義したサービス群を作成し、それぞれへタグを付与する
// クラスタ、タスク定義、サービスのうち既存のものは変更しない
// タグは常に再付与する
// Floci のタグの保持は、describe の対象となるリソースと異なり、`docker compose down`/`up` をまたいで維持されないためである
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
// family が存在しない場合は、ダミーのタスク定義を登録する
// ダミーは nginx コンテナ 1 つからなり、実行しないためイメージの取得も発生しない
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

// 指定した名前の既存の ACTIVE なサービスの ARN を返す。存在しない場合は作成する
// 作成時は desired count 1、ダミーのサブネット上とする。Floci は VPC を持たないため、スケジュールは発生しない
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
