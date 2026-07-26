# DynamoDB テーブル構造

state を保持する DynamoDB テーブル 1 つのキー配置とアイテム形式。スキーマ・キー配置・アイテムの Go 表現はすべて `internal/state` に閉じており、`internal/core/model` はドメイン型のみを持ち保存形式を知らない。

## テーブル定義

| 項目 | 値 |
|---|---|
| パーティションキー | `pk`(String)のみ |
| ソートキー | なし |
| GSI / LSI | なし |
| 課金モード | 任意(オンデマンド推奨) |
| TTL | 属性 `expires_at`(Number, epoch seconds)。`override#` アイテムのみが持つ |

ソートキーが無いため、アイテム種別と対象の識別はすべて `pk` 文字列のプレフィックス(`group#` / `override#` / `status#`)とその後続部分でエンコードされる。

## アイテム種別一覧

| アイテム | `pk` の形式 | 書き手 | 読み手 |
|---|---|---|---|
| グループ設定 | `group#<名前>` | `cheapskate-cli` / Web コンソール | reconciler、CLI、Web コンソール |
| Override | `override#<名前>` | 同上 | 同上 |
| Status(リソース単位) | `status#<種別>#<ref>` | reconciler | CLI、Web コンソール |
| Status(グループ単位) | `status#group#<名前>` | reconciler | CLI、Web コンソール |

## group# — グループ設定

ドメイン表現は `model.GroupSpec` である。グループ名は `pk` からではなく `Name` フィールドで持つ。

| 属性 | 型 | 意味 |
|---|---|---|
| `pk` | S | `group#<名前>` |
| `mode` | S | `pinned` \| `schedule` \| `disabled`。未設定は `disabled` 扱い |
| `desired` | S | `mode: pinned` 時のみ意味を持つ。`running` \| `stopped` |
| `start_cron` / `stop_cron` | S | `mode: schedule` 時のみ。5 フィールド cron 式 |
| `timezone` | S | IANA タイムゾーン名。未設定時は reconciler の `DEFAULT_TIMEZONE` |
| `tag_key` / `tag_value` | S | セレクタのタグ条件 |
| `types` | SS | セレクタの対象リソースタイプ |

グループ名は `[A-Za-z0-9][A-Za-z0-9._-]{0,63}` である。`#` は `pk` の区切り文字であり、`/` は ECS の `ref` と衝突しうるため、いずれも含められない。

セレクタ未設定(`tag_key`/`tag_value`/`types` がすべて空)のグループを作成できる。ただし `mode` を `pinned`/`schedule` にするにはセレクタを要する。空の StringSet は DynamoDB 上表現できないため、セレクタ未設定時は `types` 属性自体を省略する。

## override# — 期限付きの上書き

ドメイン表現は `model.Override` である。

| 属性 | 型 | 意味 |
|---|---|---|
| `pk` | S | `override#<名前>` |
| `desired` | S | `running` \| `stopped` |
| `expires_at` | N | epoch seconds。DynamoDB TTL の対象属性 |

TTL 削除は非同期であり最大 48 時間の遅延がありうるため、読み取り側が `expires_at <= now` のアイテムを期限切れとして無視する。TTL 自体は掃除のためだけにある。

## status#<種別>#<ref> — リソース単位の実行結果

ドメイン表現は `model.Status` である。値は最後にアクションまたはエラーが発生した時点のスナップショットであり、ライブ状態ではない。

| 属性 | 型 | 意味 |
|---|---|---|
| `observed_state` | S | 直近のアクション時に観測した実状態 |
| `last_action` | S | 直近に実行したアクション |
| `last_action_at` | S | 上記の時刻(RFC3339) |
| `last_error` | S | 直近のエラー内容 |
| `last_error_at` | S | 上記の時刻(RFC3339) |
| `transitioning_since` | S | 継続中の遷移の開始時刻(RFC3339)。他の属性と違いスナップショットではなく、遷移が解消した時点で消える |

`<種別>#<ref>` は `model.Resource.ID()` が生成する識別子であり、`internal/aws/tagging` が ARN から導出する。

| 種別 | `ref` の形式 | `pk` の例 |
|---|---|---|
| `rds-instance` | DB インスタンス識別子 | `status#rds-instance#dev-db` |
| `rds-cluster` | クラスター識別子 | `status#rds-cluster#dev-cluster` |
| `ecs-service` | `<クラスター名>/<サービス名>` | `status#ecs-service#dev-cluster/api` |
| `ec2-instance` | インスタンス ID | `status#ec2-instance#i-0abc123` |

### 書き込み

書き込みは `UpdateItem` の `SET` で行う。`PutItem` による全置換ではないため、一部の属性だけを更新して他を残す部分更新ができる。

更新する属性は `state.StatusPatch` で指定する。各フィールドはポインタであり、`nil` は該当属性に触らないこと、`state.Set("")` は該当属性を空にすることを意味する。属性名を知るのは `internal/state` だけであり、アプリケーション層が DynamoDB の属性名を文字列で組み立てる経路は存在しない。

### 削除

セレクタに一致しなくなったリソースのアイテムは自動削除されない。残存しても動作に影響しない。削除は診断経由の孤立レコード削除のみが行う([overview.md](overview.md))。

## status#group#<名前> — グループ単位の実行結果

属性形は `status#<種別>#<ref>` と同一である。対象は個々のリソースではなくグループの処理そのものであり、cron・timezone の不正、検出の失敗、セレクタの重複といった、そのグループの設定に由来する失敗を記録する。

`"group"` はリソースタイプの定数として使われないため、実リソースの `pk` 空間と衝突しない。グループの削除時に `group#`・`override#` とあわせて削除され、リソース単位の `status#` は対象外である。

セレクタの重複をリソース側ではなくこちらに記録する。共有アイテムへ書くと、そのリソースを所有するグループのエラークリアと、無視される側のグループのエラー記録が同じ 1 件を毎サイクル奪い合い、通知が発振するためである。

## 読み書きマトリクス

| アイテム | reconciler | `cheapskate-cli` / Web コンソール |
|---|---|---|
| `group#<名前>` | 読み取りのみ | 読み書き、グループ削除時に削除 |
| `override#<名前>` | 読み取りのみ | 読み書き、明示的な解除とグループ削除時に削除 |
| `status#<種別>#<ref>` | 書き込みのみ | 読み取りのみ、孤立レコードのみ削除 |
| `status#group#<名前>` | 書き込みのみ | 読み取りのみ、グループ削除時と孤立レコードのみ削除 |

この分離は 3 段構えで担保する。

| 段 | 担保の内容 |
|---|---|
| 型 | `internal/state` への窓口は利用側が自分の必要分だけを宣言したインターフェースである。`reconcile.Store` には `PutGroup`/`PutOverride` が無く、`groups.Store` と `doctor.Store` には `UpdateStatus` が無い |
| コード | 上記の結果として、reconciler から設定を書く経路も、CLI と Web コンソールから status を書く経路もコンパイルできない |
| IAM | reconciler の実行ロールに `dynamodb:PutItem` を付与せず、`UpdateItem` も `dynamodb:LeadingKeys` 条件で `status#*` に閉じる。CLI と Web コンソールの実行ロールには `UpdateItem` を付与しない |

## アクセスパターン

ソートキーと GSI が無いため、あるグループの全属性の取得は本来 `GetItem` を 3 回要する。これを避けるため、テーブル全体を読む操作は 1 回の `Scan`(ページネーションあり)で完結させ、`pk` プレフィックスで種別を判別しつつグループ名でメモリ上 join する。個別のグループ 1 件だけを操作する場合は `GetItem` と `PutItem` を使う。
