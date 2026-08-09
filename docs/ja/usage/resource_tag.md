# AWS リソースタグによる設定

本ドキュメントは、管理対象の AWS リソース自身に付与するタグを規定する。タグには、管理対象を決めるセレクタのタグと、動作のパラメータを与えるパラメータのタグの 2 種類がある。それぞれの役割を、以下に示す。

| 種類 | キー | 役割 |
|---|---|---|
| セレクタのタグ | グループ側で任意に決める | 付与したリソースがそのグループの管理対象になる |
| パラメータのタグ | `cheapskate/` 始まりの固定 | 動作のパラメータを与える。未設定でも既定値で動作する |

いずれもリソース側の操作はタグの付与と削除のみであり、DynamoDB 側の操作を伴わない。反映は次の reconcile サイクルからとなる。

## セレクタのタグ

グループのセレクタはタグのキー/値と対象リソースタイプの組であり、その両方に一致するリソースが管理対象となる。

```console
cheapskate-cli set-selector --group dev --tag-key cheapskate:group --tag-value dev --types rds-cluster,ecs-service
```

この設定に対してリソース側に付与するタグは `cheapskate:group=dev` である。キーと値は任意であり、`cheapskate` 由来である必要はない。既存の `Env=dev` などをそのまま利用してもよい。

## パラメータのタグ

認識するタグキーと、その効果を、以下に示す。

| タグキー | 対象 | 効果 |
| --- | --- | --- |
| `cheapskate/desired-count` | ecs-service | 起動時に設定する desiredCount。未設定なら 1 |
| `cheapskate/scaling-min` | ecs-service | Application Auto Scaling 使用時、起動時に設定する最小容量。未設定なら desiredCount と同値 |
| `cheapskate/scaling-max` | ecs-service | Application Auto Scaling 使用時、起動時に設定する最大容量。未設定なら desiredCount と同値 |

これらをグループ側の属性ではなくリソース側のタグとしているのは、1 つのセレクタが複数の ECS サービスに一致しうるためである。

### 値の規則

値が満たすべき規則を、以下に示す。

| 項目 | 規則 |
|---|---|
| 型 | 非負の整数。空文字列は未設定として扱う |
| `cheapskate/desired-count` | 0 以下は不可 |
| 3 つの関係 | `scaling-min <= desired-count <= scaling-max` を満たすこと |

規則を満たさない値は起動時のエラーとなり、AWS API は呼ばれない。エラーは `status#` の `last_error` に記録される。範囲外の desiredCount を許容すると、指定した台数へ変更した直後に Auto Scaling が上下限まで引き戻し、指定が実現しないためである。

現在の設定値は、`cheapskate-cli show --group <名前>` の `resources[].config` および Web コンソールのグループページで確認できる。

### 付与のタイミング

> [!IMPORTANT]
> タグは初めて停止させる前に付与すること。ECS の起動は、停止時に保存した値の復元ではなく、上記タグの値からの復元である。未設定のまま停止した場合、起動時の desiredCount は 1(min/max も同値)となり、元の値は復元できない。

```console
aws ecs tag-resource --resource-arn <サービス ARN> --tags \
  key=cheapskate:group,value=dev \
  key=cheapskate/desired-count,value=2 \
  key=cheapskate/scaling-min,value=1 \
  key=cheapskate/scaling-max,value=4
```
