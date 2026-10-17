# ECS service の設定

本ドキュメントは、ECS service に適用する DynamoDB のグループ設定、cheapskate-cli の操作、および起動・スケーリング用タグの設定を示す。cheapskate のリソースタイプ名は ecs-service である。

## 設定の目的

起動台数は停止状態から何台で再開するか、通常の上下限は稼働中に許容する台数の範囲を指定する。上下限管理モードは、タグの範囲を継続して維持する tags と、起動後の上下限変更を AWS Scheduled Scaling に委ねる scheduled を選ぶ。管理範囲と使い分けは、[ECS の機能の目的](../ecs.md#機能の目的と管理範囲) を参照。

## 対象条件

REPLICA の ECS service が対象である。cluster 名を含む長形式の ARN を使用する。cluster や task definition に付けたタグは、service 自身の設定の代わりにはならない。

以下の例の CLI context と AWS 接続先は、[共通の前提](README.md#共通の前提) を参照。

## DynamoDB と CLI

スケジュールと override は、[共通の DynamoDB 設定](README.md#共通の-dynamodb-設定) に従う。ECS service では、追加で次の属性を所属グループに登録する。

| 属性 | DynamoDB の型 | 用途と制約 |
| --- | --- | --- |
| ecs_max_count | Number | 各 service の cheapskate:desired-count と cheapskate:scaling-max に許可する上限。1～2147483647 |

ecs_max_count は、起動と稼働中の設定更新に必要である。停止にはこの上限を適用しない。停止だけを行うグループでは省略できるが、属性型や値が不正なグループでは停止も実行しない。

上限は各 service に適用し、グループ全体の合計台数には適用しない。Scheduled Scaling が設定する上下限や現在台数を制限する値でもない。

共通の CLI 操作でグループ dev を作成してから、service の所属タグを設定する前に上限を登録する。上限 3 を登録するコマンドを、以下に示す。

```sh
cheapskate-cli -context dev ecs-max-count --group dev 3
```

ecs-max-count は既存グループの上限だけを登録・変更し、スケジュールと override を保持する。保存される属性は、DynamoDB JSON では次のようになる。

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

スケジュールと override の登録・変更は、[共通の CLI 操作](README.md#共通の-cli-操作) を参照。

## タグ側の設定

共通の cheapskate:group に加え、ECS service 自身に設定するタグを、以下に示す。値はすべて文字列である。

| タグ | 既定値 | 意味と制約 |
| --- | --- | --- |
| cheapskate:desired-count | 1 | 停止状態または上下限 0/0 からの起動台数。1～2147483647 |
| cheapskate:scaling-min | cheapskate:desired-count | 通常の MinCapacity。0～2147483647 |
| cheapskate:scaling-max | cheapskate:desired-count | 通常の MaxCapacity。0～2147483647 |
| cheapskate:scaling-bounds-mode | scheduled | 上下限の管理方法。tags はタグ範囲を維持して Scheduled Scaling を一時停止、scheduled は起動後の上下限変更を維持して Scheduled Scaling を有効化 |

既定値はタグが存在しない場合だけ適用し、空文字列はエラーとなる。各タグの台数は cheapskate:scaling-min <= cheapskate:desired-count <= cheapskate:scaling-max を満たす必要がある。起動と稼働中の設定更新には、cheapskate:desired-count と cheapskate:scaling-max のタグ値が、DynamoDB の ecs_max_count 以下であることも必要である。scalable target の有無にかかわらずタグ一式を検証し、不正なタグがあれば停止も実行しない。

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

cheapskate:desired-count は起動時の台数であり、稼働中の台数を常に固定する値ではない。scalable target がなければ、停止は desiredCount=0、起動は全台数が 0 のときに起動台数を設定する。cheapskate は scalable target を新規作成しない。

scalable target がある場合は、停止時に上下限を 0/0 とし、Scheduled Scaling を一時停止する。起動時は起動台数を設定してから通常の上下限と管理モードに対応する一時停止状態を適用する。稼働中は tags モードなら通常の上下限を維持し、scheduled モードで AWS の一時停止も解除済みなら Scheduled Scaling が変更した上下限と台数を維持する。

確認コマンドは、[共通の反映確認](README.md#共通の反映確認) を参照。show の JSON 出力で、group.ecs_max_count と、対象リソースの config、live.detail の台数・上下限・ScheduledScalingSuspended、および live_error を確認する。停止完了には ECS の desiredCount、runningCount、および pendingCount がすべて 0 である必要がある。

タグ変更時と途中失敗後の挙動の詳細は、[ECS の起動・停止とスケーリング](../ecs.md) を参照。管理終了時に残る上下限と一時停止の扱いは、[日常の操作](../operations.md#管理の終了) を参照。
