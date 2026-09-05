# リソースタグ

## グループへの所属

管理する各リソースに、次の固定タグを 1 個設定する。

```text
cheapskate:group=<グループ名>
```

reconciler は、値を指定しない `cheapskate:group` tag filter で Resource Groups Tagging API の `GetResources` を呼び出す。1 ページあたり最大 100 リソースを要求し、全ページを読み込む。所属先は、各 ARN とともに返されたタグ値だけで決定する。

## ECS の復元設定

ECS には停止状態がないため、cheapskate は service を 0 まで scale in し、起動時に service 自身のタグから値を復元する。使用するタグを次の表に示す。

| タグ | 必須 | 意味 |
| --- | --- | --- |
| `cheapskate/desired-count` | いいえ。既定値 `1` | Start 後の desired task 数。正の int32 |
| `cheapskate/scaling-min` | いいえ。既定値は desired count | 復元する minimum capacity。0 以上の int32 |
| `cheapskate/scaling-max` | いいえ。既定値は desired count | 復元する maximum capacity。0 以上の int32 |

scalable target がある場合は、3 個すべての値が `0 <= min <= desired <= max` を満たす必要がある。Stop は scalable target の上下限だけを `0/0` に変更する。Start は min/max を復元してから desired count を復元する。

scalable target がない場合、Stop は desired count を 0 にし、Start は設定値または既定値へ戻す。scalable target がなくても、3 個のタグ値をすべて検証する。

ECS または Application Auto Scaling の変更 API を呼び出す前に、タグ一式を検証する。`REPLICA` 以外の scheduling strategy を使用する service はエラーになる。
