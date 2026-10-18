# リソース別の設定と共通項目

本ドキュメントはリソースタイプ別の設定と、全タイプに共通する設定・操作の参照先を示す。対象リソースの文書から、タグの設定先と固有の条件を確認する。

## リソースタイプ

設定対象と文書の対応を、以下に示す。

| リソース | cheapskate のリソースタイプ名 | タグの設定先 | 固有の設定 |
| --- | --- | --- | --- |
| [RDS DB インスタンス](rds-instance.md) | rds-instance | DB インスタンス | なし |
| [Aurora DB クラスタ](rds-cluster.md) | rds-cluster | DB クラスタ | なし |
| [EC2 インスタンス](ec2-instance.md) | ec2-instance | EC2 インスタンス | Auto Scaling グループ所属を示すタグによる除外 |
| [ECS service](ecs-service.md) | ecs-service | ECS service | グループの台数上限、起動台数、通常の上下限、Scheduled Scaling の一時停止 |

## 共通の設定と操作

DynamoDB には 1 グループにつき 1 アイテムを保存する。CLI は設定を保存し、後続の reconciler サイクルが AWS へ反映する。共通項目の参照先を、以下に示す。

| 項目 | 参照先 |
| --- | --- |
| <a id="共通の前提"></a>接続先、タイムゾーン、context、AWS profile | [CLI 設定例の前提](../cli_records.md#前提) |
| テーブルと実行環境 | [セットアップ](../setup.md) |
| <a id="共通の-dynamodb-設定"></a>属性、値の制約、適用範囲 | [グループ設定](../group_config.md) |
| <a id="共通の-cli-操作"></a>登録・変更と保存される値 | [CLI によるグループ設定例](../cli_records.md) |
| ECS 台数上限の操作 | [ECS service の DynamoDB と CLI](ecs-service.md#dynamodb-と-cli) |
| <a id="共通の所属タグ"></a>所属タグの設定先、値、検出方法 | [リソースタグ](../resource_tag.md) |
| <a id="共通の反映確認"></a>状態とエラーの確認 | [日常の操作](../operations.md#設定と現在状態の確認) |
| 出力フィールド | [CLI の出力形式](../cli.md#出力形式) |
| 反映タイミングと失敗時の扱い | [起動・停止の挙動](../resource_flow.md) |
| 管理の終了手順 | [管理の終了](../operations.md#管理の終了) |

タイプ別の ARN とタグ付けコマンドは、上記の各リソース文書に示す。
