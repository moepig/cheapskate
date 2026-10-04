# DynamoDB データモデル

state テーブルのパーティションキーは String 型の `pk`、ソートキーは String 型の `sk` である。セカンダリインデックスと TTL は使用しない。

保存するのはグループアイテムだけである。1 グループを `pk=CONFIG`、`sk=GROUP#<名前>` の 1 アイテムで表す。属性を次の表に示す。

| 属性 | 型 | 意味 |
| --- | --- | --- |
| `pk` | S | `CONFIG` |
| `sk` | S | `GROUP#<名前>` |
| `start_cron` | S | 起動 schedule。`stop_cron` と同時に存在する |
| `stop_cron` | S | 停止 schedule。`start_cron` と同時に存在する |
| `override` | S | `running`、`stopped`、`disabled` のいずれか |
| `override_expires_at` | N | 任意の Unix time 秒 |
| `ecs_max_count` | N | 任意の正の int32。所属する各 ECS サービスの起動台数と scaling-max の上限 |

未知属性と不正な属性型は、そのグループの設定エラーである。失効済み override は正常な保存データとして扱い、望ましい状態の決定では無視する。

`ecs_max_count` はグループ全体の合計台数ではなく、ECS サービスごとの上限である。ECS の Start にはこの属性の登録が必要であり、未登録またはタグ値の超過は更新 API を呼ぶ前にエラーとなる。Stop にはこの上限を適用しない。0、負数、小数、int32 の範囲外、および Number 型以外の値は保存データの設定エラーとなる。

全グループの列挙では、`CONFIG` に対する強い整合性の Query を使用する。設定変更では、最初に強い整合性の GetItem を行い、属性の復号を検証してから操作別の条件付き書き込みを行う。schedule、override、および clear-override では変更後のグループ全体を検証する。remove では cron と属性間の不変条件を検証しない。

既存アイテムの更新は対象属性だけを変更するため、異なる属性への同時変更を維持できる。条件不一致は競合として返し、自動的には再試行しない。

作成の条件はアイテムの不存在、既存の schedule 更新・無期限 override 更新・削除の条件はアイテムの存在である。期限付き override と clear-override は、両方の cron 属性の存在も条件とする。値や revision の比較は行わないため、同じ属性への変更は競合にならず、後に適用された値が残る。
