// cheapskate の合成ルートであり、アプリケーション層 (internal/app) と AWS アダプタ (internal/aws) の双方をインポートできる唯一のパッケージ
// 各アダプタを、それが満たすポートへ束ねる
// cmd/ の各エントリポイントは、依存をここから組み立てる
// これにより、AWS クライアントとポートの対応づけは、3 つのバイナリに重複せず 1 か所に収まる
package wire

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	aas "github.com/aws/aws-sdk-go-v2/service/applicationautoscaling"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/sns"

	"cheapskate/internal/app/port"
	"cheapskate/internal/aws/compute"
	awssns "cheapskate/internal/aws/sns"
	"cheapskate/internal/aws/tagging"
	"cheapskate/internal/core/model"
)

// Resource Groups Tagging API に裏打ちされた port.Discoverer を組み立てる
func Discoverer(cfg aws.Config) port.Discoverer {
	return &tagging.Discoverer{Client: resourcegroupstaggingapi.NewFromConfig(cfg)}
}

// reconciler が必要とする読み書き可能なターゲット一式を、model.Type* をキーにして組み立てる
func Targets(cfg aws.Config) map[model.ResourceType]port.Target {
	rdsClient := rds.NewFromConfig(cfg)
	targets := []port.Target{
		&compute.RdsInstanceTarget{Client: rdsClient},
		&compute.RdsClusterTarget{Client: rdsClient},
		&compute.EcsServiceTarget{Ecs: ecs.NewFromConfig(cfg), AutoScaling: aas.NewFromConfig(cfg)},
		&compute.Ec2InstanceTarget{Client: ec2.NewFromConfig(cfg)},
	}
	m := make(map[model.ResourceType]port.Target, len(targets))
	for _, t := range targets {
		m[t.Type()] = t
	}
	return m
}

// フロントエンドが必要とする読み取り専用の describer 一式を、model.Type* をキーとして組み立てる
// 値は Targets が用いるのと同じ compute の型であり、Stop/Start も実装する
// ただし、受け渡しは常により狭い port.Describer を通じて行う
// したがって cheapskate-cli と web console は、コントロールプレーンを変更する経路を持たない
// EcsServiceTarget の AutoScaling クライアントは未設定とする
// これを参照するのは Start/Stop のみであり、このマップはそのいずれにも用いないためである
func Describers(cfg aws.Config) map[model.ResourceType]port.Describer {
	targets := Targets(cfg)
	m := make(map[model.ResourceType]port.Describer, len(targets))
	for typ, t := range targets {
		m[typ] = t
	}
	return m
}

// topicArn 向けの port.Notifier を組み立てる
// ARN が空の場合は、何も行わない notifier となる
func Notifier(cfg aws.Config, topicArn string) port.Notifier {
	return &awssns.Notifier{Client: sns.NewFromConfig(cfg), TopicArn: topicArn}
}
