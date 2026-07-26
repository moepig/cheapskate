// アプリケーション層が外界に求めるものをすべて宣言する
// 実装は internal/aws 配下にあり、internal/wire がこれらのインターフェースへ束ねる
// そのため internal/app が internal/aws をインポートすることはなく、依存の向きは内側を指す
// アプリ層のテストに必要なのは porttest サブパッケージにある手書きのテストダブルだけで済む
//
// state テーブルは意図的にポートにしていない
// 差し替え可能な依存ではなく cheapskate に固有のものなので、internal/state はアプリ層の具体的な協働相手として扱う
// テストではその下にある DynamoDB クライアントの側をモックする
//
// ただし「差し替えないこと」は「何でも呼べること」とは別である
// state への窓口は利用側の各パッケージが自分の必要な範囲だけのインターフェースとして宣言する
// （reconcile.Store・groups.Store・doctor.Store）
// これにより reconciler は設定アイテムを書けず、設定フロントエンドはステータスを書けない、が構造として成り立つ
// Target から Describer を絞り込んでいるのと同じ理由による（下の Describer を参照）
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
	// res.Tags は AWS リソース自身のタグから読んだターゲット固有の起動設定を運ぶ（ECS なら model.EcsDesiredCountTagKey など）
	// cheapskate は停止時に状態を記録しないため、復元元となる保存済みの状態は存在しない
	Start(ctx context.Context, res model.Resource) error
}

// 種別ごとの読み取り専用 Describe API 経由で、リソースの現在の状態を都度問い合わせる
// 呼ぶ API は ec2:DescribeInstances、rds:DescribeDBInstances/DescribeDBClusters、ecs:DescribeServices
// Target.Describe と同じ呼び出しだが Stop/Start を持たない
// これにより読み取り専用のフロントエンド（webconsole、`cheapskate-cli show`）はコントロールプレーンを変更する経路を持たない
// すべての Target がこのインターフェースをそのまま満たし、一方から他方へ絞り込むのは internal/wire である
type Describer interface {
	Describe(ctx context.Context, ref string) (model.Observation, error)
}

// アクションと失敗のときだけ publish する
// internal/aws/sns が実装する
type Notifier interface {
	Publish(ctx context.Context, subject string, payload map[string]any) error
}
