# cheapskate

RDS インスタンス / Aurora クラスター / ECS サービス / EC2 インスタンスの起動・停止を管理するツール。望ましい状態を DynamoDB に保持し、Lambda の reconcile ループで実状態を収束させる。

機能:

- RDS/Aurora を 7 日間の自動起動制限を超えて停止し続ける
- cron によるスケジュール起動・停止
- AWS タグのセレクタによる管理対象の動的な決定
- 望ましい状態が `running` のリソースを操作しないこと

自動起動された RDS/Aurora は、完了イベント(`RDS-EVENT-0088` / `RDS-EVENT-0151`)を検知して再停止する。スケジュールと停止維持は同じ reconcile ループで処理されるため、競合しない。設定の単位はターゲットグループであり、リソースの個別登録は存在しない。

構成: コントロールプレーンは 5 分間隔の Lambda 1 つと DynamoDB 1 テーブル。

配布形態: ソースコードのみ。IaC テンプレート・公開コンテナイメージは提供しない。イメージ(reconciler 用とオプションの Web コンソール用の 2 つ)は各自ビルドして自分の ECR に push し、AWS リソースは自前の IaC または手動で作成する([usage/setup.md](usage/setup.md))。

## ドキュメント

Architecture — 仕組み:

- [architecture/overview.md](architecture/overview.md) — 全体構成、データモデル、reconcile ループ
- [architecture/aws_resources.md](architecture/aws_resources.md) — AWS リソース構成図(reconcile ループ / Web コンソール)
- [architecture/database.md](architecture/database.md) — DynamoDB テーブルのアイテム構造
- [architecture/trigger.md](architecture/trigger.md) — 呼び出し経路と SNS 通知
- [architecture/logging.md](architecture/logging.md) — 各コンポーネントのログ形式とイベント一覧
- [architecture/metrics.md](architecture/metrics.md) — EMF メトリクスと Lambda 組み込みメトリクスの分担
- [architecture/cheapskate-cli.md](architecture/cheapskate-cli.md) — CLI の設計
- [architecture/web_console.md](architecture/web_console.md) — Web コンソールの設計
- [architecture/on_lambda.md](architecture/on_lambda.md) — Lambda 上での実行形態。RIE / LWA の外形と各コンポーネントへの組み込み
- [architecture/emulation_local.md](architecture/emulation_local.md) — ローカルエミュレーションの構成

Usage — 自分の AWS アカウントへのホスティング:

- [usage/concepts.md](usage/concepts.md) — 用語と全体像
- [usage/setup.md](usage/setup.md) — 作成するリソースの仕様(IAM ポリシー、実行例)
- [usage/operations.md](usage/operations.md) — 設定レコードの追加・変更・確認・削除と、設定できる項目
- [usage/troubleshooting.md](usage/troubleshooting.md) — 障害の検知、中途半端に終わった処理の復旧、`doctor` によるゴミデータの掃除
- [usage/config.md](usage/config.md) — 環境変数のリファレンス
- [usage/resource_tag.md](usage/resource_tag.md) — AWS リソースに付与するタグ(セレクタ / ECS のスケーリング)

Development — cheapskate 自体の開発:

- [development/build.md](development/build.md) — バイナリとコンテナイメージ(reconciler / webconsole)のビルド
- [development/test.md](development/test.md) — ユニット/統合テストと lint
- [development/mock.md](development/mock.md) — モックの生成方法と使い分け
- [development/run_local.md](development/run_local.md) — ローカル実行

## ライセンス

MIT
