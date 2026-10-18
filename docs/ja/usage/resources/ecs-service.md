# ECS service の設定

本ドキュメントはECS service に適用する DynamoDB のグループ設定、cheapskate-cli の操作、および起動・スケーリング用タグの設定を示す。cheapskate のリソースタイプ名は ecs-service である。

## 設定の目的

起動台数は停止状態から何台で再開するか、通常の上下限は稼働中に許容する台数の範囲を指定する。上下限管理モードはタグの範囲を継続して維持する tags と、起動後の上下限変更を AWS Scheduled Scaling に委ねる scheduled を選ぶ。管理範囲と使い分けは、[ECS の機能の目的](../ecs.md#機能の目的と管理範囲) を参照。

## 対象条件

REPLICA の ECS service が対象である。cluster 名を含む長形式の ARN を使用する。cluster や task definition に付けたタグは、service 自身の設定の代わりにはならない。

以下の例の CLI context と AWS 接続先は、[CLI 設定例の前提](../cli_records.md#前提) を参照。

## DynamoDB と CLI

スケジュールと override の属性形式は、[グループ設定の DynamoDB 登録形式](../group_config.md#dynamodb-の登録形式) を参照。ECS service の起動と稼働中の設定更新には、グループへの `ecs_max_count` 登録が必要である。属性の型、値域、適用条件は、[ECS 台数上限](../group_config.md#ecs-台数上限) を参照。属性の型や値が不正なグループでは、停止も実行しない。

グループ dev を [CLI 設定例](../cli_records.md) の操作で作成し、service の所属タグを設定する前に上限を登録する。上限 3 を登録するコマンドを、以下に示す。

```sh
cheapskate-cli -context dev ecs-max-count --group dev 3
```

ecs-max-count は既存グループの上限だけを登録・変更し、スケジュールと override を保持する。保存される属性はDynamoDB JSON では次のようになる。

```json
{"ecs_max_count": {"N": "3"}}
```

上限を 4 に変更するコマンドを、以下に示す。

```sh
cheapskate-cli -context dev ecs-max-count --group dev 4
```

上限属性を削除するコマンドを、以下に示す。

```sh
cheapskate-cli -context dev clear-ecs-max-count --group dev
```

clear-ecs-max-count は上限を未登録に戻す。現在の service を停止する操作ではなく、上限を再登録するまで以降の起動と稼働中の設定更新がエラーとなる。0 台を維持する場合は stopped override を使用する。

スケジュールと override の登録・変更例は、[CLI 設定例](../cli_records.md) を参照。

## タグ側の設定

共通の cheapskate:group に加え、ECS service 自身に設定するタグを、以下に示す。値はすべて文字列である。

| タグ | 既定値 | 意味と制約 |
| --- | --- | --- |
| cheapskate:desired-count | 1 | 停止状態または上下限 0/0 からの起動台数。1～2147483647 |
| cheapskate:scaling-min | cheapskate:desired-count | 通常の MinCapacity。0～2147483647 |
| cheapskate:scaling-max | cheapskate:desired-count | 通常の MaxCapacity。0～2147483647 |
| cheapskate:scaling-bounds-mode | scheduled | 上下限の管理方法 |

上下限管理モードの値と動作を、以下に示す。

| 値 | 動作 |
| --- | --- |
| tags | タグ範囲を維持し、Scheduled Scaling を一時停止 |
| scheduled | 起動後の上下限変更を維持し、Scheduled Scaling を有効化 |

既定値はタグが存在しない場合だけ適用し、空文字列はエラーとなる。各タグの台数は cheapskate:scaling-min <= cheapskate:desired-count <= cheapskate:scaling-max を満たす必要がある。scalable target の有無にかかわらずタグ一式を検証し、不正なタグがあれば停止も実行しない。

cluster が dev、service が api の場合に、起動台数 2、継続して維持する上下限 1～3 を設定する例を、以下に示す。

```sh
aws ecs tag-resource --region ap-northeast-1 \
  --resource-arn arn:aws:ecs:ap-northeast-1:123456789012:service/dev/api \
  --tags key=cheapskate:group,value=dev \
    key=cheapskate:desired-count,value=2 \
    key=cheapskate:scaling-min,value=1 \
    key=cheapskate:scaling-max,value=3 \
    key=cheapskate:scaling-bounds-mode,value=tags
```

ECS のタグ引数は key と value を小文字で指定する。引数と長形式 ARN の詳細は、[AWS CLI tag-resource](https://docs.aws.amazon.com/cli/latest/reference/ecs/tag-resource.html) を参照。

## 設定の反映と確認

起動・停止、タグ変更、および部分失敗時の挙動は、[ECS の起動・停止とスケーリング](../ecs.md) を参照。状態確認コマンドは[日常の操作](../operations.md#設定と現在状態の確認) を参照。show の JSON 出力で確認する項目を、以下に示す。

| 確認箇所 | 確認する内容 |
| --- | --- |
| group.ecs_max_count | 保存されたグループの台数上限 |
| resources[].config | 対象 service のタグ設定 |
| resources[].live.detail | 台数、上下限、ScheduledScalingSuspended |
| resources[].live_error | 状態取得のエラー |

停止完了には ECS の desiredCount、runningCount、pendingCount がすべて 0 である必要がある。

管理終了時に残る上下限と一時停止の扱いは、[日常の操作](../operations.md#管理の終了) を参照。
