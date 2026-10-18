# cheapskate ドキュメント

本ドキュメントは cheapskate の導入、日常の運用、設定仕様、リソースの挙動、および開発方法の入口である。

目的に応じた参照経路を、以下に示す。

| 目的 | 参照経路 |
| --- | --- |
| 初回導入 | [概念](usage/concepts.md) → [セットアップ](usage/setup.md) |
| 日常操作 | [日常の操作](usage/operations.md) → [CLI 設定例](usage/cli_records.md) |
| 個別リソースの設定 | [リソース設定の索引](usage/resources/README.md) → 各タイプの設定 |
| 障害調査 | [トラブルシューティング](usage/troubleshooting.md) → [ログ・メトリクス](usage/monitoring.md) |
| コード読解 | [アーキテクチャ](architecture/overview.md) → [実装契約](development/reconcile.md) → [API シーケンス](development/resource_sequence.md) |

## 導入

初めて使用する場合は、グループとスケジュールの関係を確認し、セットアップの手順で最初のグループを登録する。

| ドキュメント | 内容 |
| --- | --- |
| [グループとスケジュール](usage/concepts.md) | 所属、スケジュール、override の関係 |
| [セットアップ](usage/setup.md) | AWS の準備、最初の設定、動作確認、定期実行の有効化 |
| [AWS リソースと IAM](usage/aws_resources.md) | 配置する AWS リソースの一覧 |
| [IAM 権限](usage/iam.md) | 実行ロール、参照・操作権限、通知・暗号化の追加権限 |

## 日常の運用

操作の目的に応じた手順と確認方法を、以下に示す。

| ドキュメント | 内容 |
| --- | --- |
| [日常の操作](usage/operations.md) | 確認、スケジュール変更、一時起動・停止、ECS 設定変更、管理終了 |
| [CLI によるグループ設定例](usage/cli_records.md) | コマンドと DynamoDB の登録内容を対応させた例 |
| [Terraform によるグループ設定](usage/terraform_records.md) | IaC での登録、変更、削除 |
| [Web コンソール](usage/web_console.md) | 画面、設定変更、override の有効期限の入力、公開時のアクセス設定 |
| [ログ・メトリクス・通知](usage/monitoring.md) | 現在状態、サイクルの結果、エラー検知、成功通知 |
| [トラブルシューティング](usage/troubleshooting.md) | 症状ごとの確認項目と対処 |

## 設定仕様と挙動

値の制約と、設定が実際のリソースにどう適用されるかを、以下の文書で定義する。

| ドキュメント | 内容 |
| --- | --- |
| [リソース別の設定と共通項目](usage/resources/README.md) | タイプ別設定と共通仕様・操作への参照先 |
| [実行時設定](usage/config.md) | 接続先、タイムゾーン、監視、HTTP 公開の環境変数 |
| [グループ設定](usage/group_config.md) | グループ名、cron、override、ECS 上限、DynamoDB の属性型 |
| [リソースタグ](usage/resource_tag.md) | タグの設定先、既定値、値の制約 |
| [CLI リファレンス](usage/cli.md) | 引数、出力形式、終了コード、設定更新時の制約 |
| [起動・停止の挙動](usage/resource_flow.md) | 反映タイミング、対応リソース、手動変更、失敗と同時実行 |
| [ECS の起動・停止とスケーリング](usage/ecs.md) | タスク数、上下限、一時停止、タグ変更、部分失敗の扱い |
| [ECS の設定と現在状態の具体例](usage/ecs_examples.md) | 起動、タグ変更、一時停止解除、応答消失、停止の状態例 |

## 開発

コードや実行構成を変更する場合の文書を、以下に示す。

| ドキュメント | 内容 |
| --- | --- |
| [アーキテクチャ](architecture/overview.md) | コンポーネント、データの流れ、Lambda 実行 |
| [ビルド](development/build.md) | バイナリとイメージの作成、ECR への登録 |
| [ローカル実行](development/run_local.md) | AWS エミュレータと開発環境の起動 |
| [テスト](development/test.md) | 検証対象と実行コマンド |
| [モック](development/mock.md) | テスト用クライアントと生成方法 |
| [Reconcile の実装契約](development/reconcile.md) | 読み込み境界、エラー分離、再適用、通知境界 |
| [リソース操作のシーケンス](development/resource_sequence.md) | AWS API の呼び出し順とコード読解のための分岐 |
| [GitHub Actions](development/github-actions.md) | CI と自動更新の構成 |
| [リリース](development/release.md) | リリース手順と依存更新 |

## 言語

英語版は[English documentation](../en/README.md)を参照。
