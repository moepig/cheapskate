# AWS リソースタグによる設定

管理対象の AWS リソース自身に付与するタグの一覧。用語は [concepts.md](concepts.md)、環境変数は [config.md](config.md)、グループの設定レコードの操作は [operations.md](operations.md)。

タグには 2 種類ある。

| 種類 | キー | 役割 |
|---|---|---|
| セレクタのタグ | グループ側で任意に決める | 付けたリソースがそのグループの管理対象になる |
| パラメータのタグ | `cheapskate/` 始まりの固定 | 動作のパラメータを与える。未設定でも既定値で動作する |

どちらもリソース側の操作はタグを付ける/外すだけであり、DynamoDB 側の操作を要しない。反映は次の reconcile サイクルからとなる。

## セレクタのタグ

グループのセレクタは「タグのキー/値 + 対象リソースタイプ」であり、両方に一致するリソースが管理対象になる。

```console
cheapskate-cli set-selector --group dev --tag-key cheapskate:group --tag-value dev --types rds-cluster,ecs-service
```

この場合、リソース側に付けるのは `cheapskate:group=dev` である。キーと値は任意であり、`cheapskate` 由来である必要はない(既存の `Env=dev` などをそのまま使ってもよい)。

## パラメータのタグ

| タグキー | 対象 | 効果 |
| --- | --- | --- |
| `cheapskate/desired-count` | ecs-service | 起動時に設定する desiredCount。未設定なら 1 |
| `cheapskate/scaling-min` | ecs-service | Application Auto Scaling 使用時、起動時に設定する最小容量。未設定なら desiredCount と同値 |
| `cheapskate/scaling-max` | ecs-service | Application Auto Scaling 使用時、起動時に設定する最大容量。未設定なら desiredCount と同値 |

グループ側の属性ではなくリソース側のタグとしているのは、1 つのセレクタが複数の ECS サービスに一致しうるためである。

### 値の規則

| 項目 | 規則 |
|---|---|
| 型 | 非負の整数。空文字列は未設定として扱う |
| `cheapskate/desired-count` | 0 以下は不可 |
| 3 つの関係 | `scaling-min <= desired-count <= scaling-max` を満たすこと |

規則を満たさない値は起動時のエラーになり、AWS API は呼ばれない。エラーは `status#` の `last_error` に記録される。範囲外の desiredCount を通すと、指定した台数にした直後に Auto Scaling が上下限まで引き戻し、指定が黙って実現しないためである。

現在の設定値は `cheapskate-cli show --group <名前>` の `resources[].config` と、Web コンソールのグループページで確認できる。

### 付与のタイミング

タグは初めて停止させる前に付けること。ECS の起動は、停止時に保存した値の復元ではなく、上記タグの値からの復元である。未設定のまま停止すると、起動時に戻るのは desiredCount 1(min/max も同値)となり、元の値は復元できない。

```console
aws ecs tag-resource --resource-arn <サービス ARN> --tags \
  key=cheapskate:group,value=dev \
  key=cheapskate/desired-count,value=2 \
  key=cheapskate/scaling-min,value=1 \
  key=cheapskate/scaling-max,value=4
```
