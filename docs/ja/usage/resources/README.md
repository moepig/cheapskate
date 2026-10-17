# リソース別の設定と共通項目

本ドキュメントは、全リソースに共通する DynamoDB 設定、cheapskate-cli の操作、および所属タグのルールを示す。リソースタイプ固有の設定は、配下の文書で説明する。

## リソースタイプ

設定対象と文書の対応を、以下に示す。

| リソース | タイプ名 | タグの設定先 | 固有の設定 |
| --- | --- | --- | --- |
| [RDS DB インスタンス](rds-instance.md) | rds-instance | DB インスタンス | なし |
| [Aurora DB クラスタ](rds-cluster.md) | rds-cluster | DB クラスタ | なし |
| [EC2 インスタンス](ec2-instance.md) | ec2-instance | EC2 インスタンス | Auto Scaling グループ所属を示すタグによる除外 |
| [ECS service](ecs-service.md) | ecs-service | ECS service | グループの台数上限、起動台数、通常の上下限、Scheduled Scaling の一時停止 |

## 共通の前提

各文書の例は、既存のテーブル cheapskate、リージョン ap-northeast-1、およびタイムゾーン Asia/Tokyo を使用する。reconciler の DEFAULT_TIMEZONE も Asia/Tokyo にする。dev という CLI context を登録する例を、以下に示す。

```sh
cheapskate-cli config set-context dev \
  --table cheapskate --region ap-northeast-1 --timezone Asia/Tokyo
```

各コマンドの -context dev は接続先の指定であり、--group dev は DynamoDB のグループ名の指定である。AWS の認証情報は事前に設定する。context に --profile を指定する場合、タグ設定に使う AWS CLI にも同じ --profile を指定すること。ARN、アカウント ID、およびリソース名は実際の値に置き換える。

テーブルと実行環境の準備は、[セットアップ](../setup.md) を参照。

## 共通の DynamoDB 設定

DynamoDB には 1 グループにつき 1 アイテムを保存する。リソースごとのアイテムは作成しない。同じグループの変更は、リソースタイプを問わず所属する全リソースに影響する。個別のスケジュールや override が必要な場合は、グループを分ける。

全リソースが参照する属性を、以下に示す。

| 属性 | 型 | 用途 |
| --- | --- | --- |
| pk | String | 固定値 CONFIG |
| sk | String | GROUP#<グループ名>。所属タグの値と対応する |
| start_cron、stop_cron | String | 起動・停止時刻。両方を 5 フィールド cron で指定 |
| override | String | running は起動状態、stopped は停止状態を維持。disabled は reconciler の状態確認と変更を抑止 |
| override_expires_at | Number | override の有効期限を Unix time 秒で指定。省略時は無期限 |

スケジュールまたは override の少なくとも一方が必要である。期限付き override にはスケジュールも必要である。有効な override がスケジュールに優先し、解除または失効後はその時点のスケジュールへ戻る。

ecs_max_count は ECS service のみが使用し、RDS、Aurora、および EC2 には登録不要である。ただし、属性型や値が不正な場合はグループ全体の設定エラーとなり、同じグループの他のリソースも処理しない。未使用の属性は 0 や NULL で埋めず、省略すること。

名前、cron、有効期限、および属性型の制約は、[グループ設定](../group_config.md) を参照。ECS 上限の意味と設定方法は、[ECS service の DynamoDB と CLI](ecs-service.md#dynamodb-と-cli) を参照。

## 共通の CLI 操作

グループを登録してから、各リソースへ所属タグを設定する。ECS では所属タグを設定する前に台数上限も登録する。平日 09:00～18:00 のスケジュールを登録する例を、以下に示す。

```sh
cheapskate-cli -context dev schedule --group dev \
  -start '0 9 * * 1-5' -stop '0 18 * * 1-5'
```

新規グループの保存内容を、DynamoDB JSON 形式で示す。

```json
{
  "pk": {"S": "CONFIG"},
  "sk": {"S": "GROUP#dev"},
  "start_cron": {"S": "0 9 * * 1-5"},
  "stop_cron": {"S": "0 18 * * 1-5"}
}
```

既存グループの場合、schedule は cron のみを更新し、override と ECS 上限を保持する。スケジュールが不要な場合は、無期限 override でグループを作成できる。スケジュール登録後の変更操作を、以下に示す。それぞれ独立した操作である。

| 目的 | コマンド | 保存内容 |
| --- | --- | --- |
| 2 時間起動状態を維持 | cheapskate-cli -context dev override --group dev running -for 2h | override=running と、実行時刻から 2 時間後の override_expires_at |
| 無期限の停止 | cheapskate-cli -context dev override --group dev stopped | override=stopped。有効期限は削除 |
| 自動制御の一時停止 | cheapskate-cli -context dev override --group dev disabled | override=disabled。有効期限は削除 |
| override の解除 | cheapskate-cli -context dev clear-override --group dev | override と override_expires_at を削除 |

-for を省略した running も無期限となる。clear-override にはスケジュールが必要である。失効済みの override_expires_at は自動削除されないため、次の設定変更前に override を解除するか、新しい override に置き換える。

DynamoDB のグループ設定はすべて cheapskate-cli で登録できる。ecs_max_count の操作は、[ECS service の DynamoDB と CLI](ecs-service.md#dynamodb-と-cli) を参照。コマンドの全引数と終了コードは、[CLI リファレンス](../cli.md) を参照。

## 共通の所属タグ

AWS リソース自身に設定する所属タグを、以下に示す。タグは AWS CLI、AWS コンソール、または IaC で設定する。

| タグ | 値 | 既定値 |
| --- | --- | --- |
| cheapskate:group | DynamoDB に登録済みのグループ名。例では dev | なし |

タグがないリソースは検出対象にならない。空文字列、不正なグループ名、または未登録のグループ名は、そのリソースのエラーとなる。設定先の ARN とタグ付けコマンドは、各リソースタイプの文書で示す。

タグの検出方法と値の制約は、[リソースタグ](../resource_tag.md) を参照。

## 共通の反映確認

cheapskate-cli はグループ設定を保存する。設定を読み取った後続の reconciler サイクルで起動・停止が始まる。CLI の成功は AWS の操作完了を意味しない。設定と現在状態を確認するコマンドを、以下に示す。

```sh
cheapskate-cli -context dev -output json show --group dev
```

group で保存設定を、resources の ref と type で対象リソースを確認する。live.state と live.detail は現在状態、live_error は取得エラーである。show は個別リソースの取得エラーを含んでも終了コード 0 を返すため、live_error も確認すること。

管理中の手動変更はグループの要求へ戻される。disabled、所属タグの削除、およびグループの削除は、現在の AWS 設定を残す。反映タイミングと失敗時の扱いは、[起動・停止の挙動](../resource_flow.md) を参照。管理終了の手順は、[日常の操作](../operations.md#管理の終了) を参照。
