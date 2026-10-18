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

## 共通の前提

接続先、タイムゾーン、CLI context、および AWS profile の準備は、[CLI 設定例の前提](../cli_records.md#前提)を参照。テーブルと実行環境の準備は、[セットアップ](../setup.md)を参照。

## 共通の DynamoDB 設定

DynamoDB には 1 グループにつき 1 アイテムを保存する。属性、値の制約、およびグループ内のリソースへの適用範囲は、[グループ設定](../group_config.md)を参照。

## 共通の CLI 操作

グループの登録・変更と保存される値の対応は、[CLI によるグループ設定例](../cli_records.md)を参照。ECS の台数上限の操作は[ECS service の DynamoDB と CLI](ecs-service.md#dynamodb-と-cli)を参照。

## 共通の所属タグ

所属タグの設定先、値の制約、および検出方法は、[リソースタグ](../resource_tag.md)を参照。タイプ別の ARN とタグ付けコマンドは、上記の各リソース文書に示す。

## 共通の反映確認

CLI は設定を保存し、後続の reconciler サイクルが AWS へ反映する。状態とエラーの確認手順は、[日常の操作](../operations.md#設定と現在状態の確認)を参照。出力フィールドの定義は[CLI の出力形式](../cli.md#出力形式)を参照。

反映タイミングと失敗時の扱いは、[起動・停止の挙動](../resource_flow.md)を参照。管理を終了する場合は、[管理の終了](../operations.md#管理の終了)に従う。
