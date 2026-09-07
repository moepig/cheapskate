# リソースタグ

## グループへの所属

管理する各リソースに、次の固定タグを 1 個設定する。

```text
cheapskate:group=<グループ名>
```

reconciler は、値を指定しない `cheapskate:group` tag filter で Resource Groups Tagging API の `GetResources` を呼び出す。1 ページあたり最大 100 リソースを要求し、全ページを読み込む。対応する RDS instance、RDS cluster、ECS service、および EC2 instance の resource type filter も指定する。所属先は、各 ARN とともに返されたタグ値だけで決定する。同じ ARN が複数回返された場合は最後のタグ一式を採用し、1 サイクルに 1 回処理する。ARN の解析に失敗した場合は探索全体が失敗する。ECS service には cluster 名を含む長形式の ARN が必要である。

## ECS の復元設定

ECS には停止状態がないため、cheapskate は service を 0 まで scale in し、起動時に service 自身のタグから値を復元する。使用するタグを次の表に示す。

| タグ | 必須 | 意味 |
| --- | --- | --- |
| `cheapskate/desired-count` | いいえ。既定値 `1` | Start 後の desired task 数。正の int32 |
| `cheapskate/scaling-min` | いいえ。既定値は desired count | 復元する minimum capacity。0 以上の int32 |
| `cheapskate/scaling-max` | いいえ。既定値は desired count | 復元する maximum capacity。0 以上の int32 |

scalable target の有無によらず、3 個すべての値が `0 <= min <= desired <= max` を満たす必要がある。タグが存在しない場合だけ既定値を使い、空文字列はエラーにする。

scalable target がある場合、Stop は上下限だけを `0/0` に変更する。Start は min/max に差がある場合だけ復元し、続いて desired count を再取得して、タグの値と異なる場合だけ更新する。上下限の変更後に desired count の更新が失敗した場合は、変更前の上下限への復元を試みる。desired count の再取得に失敗した場合は、その復元を行わずエラーを返す。

scalable target がない場合、Stop は desired count を 0 にし、Start は設定値または既定値へ戻す。scalable target がなくても、3 個のタグ値をすべて検証する。

ECS または Application Auto Scaling の変更 API を呼び出す前に、タグ一式を検証する。`REPLICA` 以外の scheduling strategy を使用する service はエラーになる。

ECS の停止判定は desired count、running count、pending count がすべて 0 であることを条件とする。それ以外は起動状態とする。起動状態と望ましい状態がともに running でも、scalable target の上下限がタグの値と異なる場合は Start を再適用する。停止状態と望ましい状態がともに stopped でも、上下限が `0/0` でなければ Stop を再適用する。起動状態で上下限が一致する場合や scalable target がない場合、desired count とタグの値の差だけでは Start を実行しない。
