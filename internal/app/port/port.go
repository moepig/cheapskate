// アプリケーション層が外界に求めるインターフェースをすべて宣言する
// 実装は internal/aws 配下にあり、internal/wire がこれらのインターフェースへ束ねる
// internal/app は internal/aws をインポートせず、依存の向きは内側を指す
// アプリ層のテストが必要とするテストダブルは、porttest サブパッケージにある
//
// state テーブルはポートとしない
// 差し替え可能な依存ではなく cheapskate に固有であるため、internal/state をアプリ層の協働相手として扱う
// テストでは、その下にある DynamoDB クライアントをモックする
//
// ただし、差し替えないことと、呼び出し範囲を限定しないことは独立している
// state への参照は、利用側の各パッケージが必要な範囲のみのインターフェースとして宣言する
// (reconcile.Store、groups.Store、doctor.Store)
// これにより、reconciler は設定アイテムを書けず、設定フロントエンドはステータスを書けない
// Target から Describer を絞り込む理由と同じである (下の Describer を参照)
package port

import (
	"context"

	"cheapskate/internal/core/model"
)

// セレクタに現在マッチするリソースをすべて見つける
// Resource Groups Tagging API を用いて internal/aws/tagging が実装する
type Discoverer interface {
	Discover(ctx context.Context, sel model.Selector) ([]model.Resource, error)
}

// リソース種別 1 つ分の describe/stop/start 操作を抽象化する
// internal/aws/compute が model.Type* 定数ごとに 1 つの値として実装する
type Target interface {
	Type() model.ResourceType
	Describe(ctx context.Context, ref string) (model.Observation, error)
	Stop(ctx context.Context, ref string) error
	// res を再び起動する
	// res.Tags は、AWS リソース自身のタグから読んだ種別固有の起動設定を保持する
	// cheapskate は停止時に状態を記録しないため、復元元となる保存済みの状態は存在しない
	Start(ctx context.Context, res model.Resource) error
}

// 種別ごとの読み取り専用 Describe API を通じて、リソースの現在の状態を問い合わせる
// 呼び出す API は ec2:DescribeInstances、rds:DescribeDBInstances/DescribeDBClusters、ecs:DescribeServices である
// Target.Describe と同じ呼び出しであり、Stop/Start を持たない
// これにより読み取り専用のフロントエンドは、コントロールプレーンを変更する経路を持たない
// すべての Target がこのインターフェースを満たし、絞り込みは internal/wire が行う
type Describer interface {
	Describe(ctx context.Context, ref string) (model.Observation, error)
}

// アクションと失敗のときだけ publish する
// internal/aws/sns が実装する
type Notifier interface {
	Publish(ctx context.Context, subject string, payload map[string]any) error
}
