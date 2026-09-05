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

未知属性と不正な属性型は、そのグループの設定エラーである。失効済み override は正常な保存データとして扱い、望ましい状態の決定では無視する。

全グループの列挙では、`CONFIG` に対する強い整合性の Query を使用する。設定変更では、最初に強い整合性の `GetItem` を行い、アイテム全体を検証してから操作別の条件付き書き込みを行う。

既存アイテムの更新は対象属性だけを変更するため、異なる属性への同時変更を維持できる。条件不一致は競合として返し、自動的には再試行しない。
