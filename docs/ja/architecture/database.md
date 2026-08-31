# DynamoDB テーブル構造

state を保持する DynamoDB テーブル 1 つのキー配置とアイテム形式。スキーマ・キー配置・アイテムの Go 表現はすべて `internal/state` に閉じており、`internal/core/model` はドメイン型のみを持ち保存形式を知らない。

## テーブル定義

テーブルに要求する定義を、以下に示す。

| 項目 | 値 |
|---|---|
| パーティションキー | `pk`(String) |
| ソートキー | `sk`(String) |
| GSI / LSI | なし |
| 課金モード | 任意(オンデマンド推奨) |
| TTL | 属性 `expires_at`(Number、epoch 秒)。override と reconcile リースが持つ |

設定は `pk=CONFIG` にまとめ、`sk` の `GROUP#` / `OVERRIDE#` で種別を分ける。Status はリソースごとに `pk=STATUS#<リソース ID>` を持ち、現在値を `sk=CURRENT` に置く。全体 reconcile のリースは `pk=LOCK, sk=RECONCILE` である。

## アイテム種別一覧

保持するアイテムは 5 種類である。キーと書き手・読み手を、以下にまとめる。

| アイテム | `pk` | `sk` | 書き手 | 読み手 |
|---|---|---|---|---|
| グループ設定 | `CONFIG` | `GROUP#<名前>` | `cheapskate-cli` / Web コンソール | reconciler、CLI、Web コンソール |
| Override | `CONFIG` | `OVERRIDE#<名前>` | 同上 | 同上 |
| Status(リソース単位) | `STATUS#<種別>#<ref>` | `CURRENT` | reconciler | CLI、Web コンソール |
| Status(グループ単位) | `STATUS#group#<名前>` | `CURRENT` | reconciler | CLI、Web コンソール |
| Reconcile リース | `LOCK` | `RECONCILE` | reconciler | reconciler |

## group# — グループ設定

ドメイン表現は `model.GroupSpec` である。保存時はグループ名を `sk` に含め、読み取り時に `Name` フィールドへ戻す。属性を、以下に示す。

| 属性 | 型 | 意味 |
|---|---|---|
| `pk` | S | `CONFIG` |
| `sk` | S | `GROUP#<名前>` |
| `mode` | S | `pinned` \| `schedule` \| `disabled`。未設定は `disabled` 扱い |
| `desired` | S | `mode: pinned` 時のみ意味を持つ。`running` \| `stopped` |
| `start_cron` / `stop_cron` | S | `mode: schedule` 時のみ。5 フィールド cron 式 |
| `timezone` | S | IANA タイムゾーン名。未設定時は reconciler の `DEFAULT_TIMEZONE` |
| `tag_key` / `tag_value` | S | セレクタのタグ条件 |
| `types` | SS | セレクタの対象リソースタイプ |

グループ名は `[A-Za-z0-9][A-Za-z0-9._-]{0,63}` である。`#` は `sk` の区切り文字であり、`/` は ECS の `ref` と衝突しうるため、いずれも含められない。

セレクタ未設定(`tag_key`/`tag_value`/`types` がすべて空)のグループを作成できる。ただし `mode` を `pinned`/`schedule` にするにはセレクタを要する。空の StringSet は DynamoDB 上表現できないため、セレクタ未設定時は `types` 属性自体を省略する。

## override# — 期限付きの上書き

ドメイン表現は `model.Override` である。属性を、以下に示す。

| 属性 | 型 | 意味 |
|---|---|---|
| `pk` | S | `CONFIG` |
| `sk` | S | `OVERRIDE#<名前>` |
| `desired` | S | `running` \| `stopped` |
| `expires_at` | N | epoch seconds。DynamoDB TTL の対象属性 |

読み取り側が `expires_at <= now` のアイテムを期限切れとして無視する。TTL 削除は非同期であり、最大 48 時間の遅延がありうるためである。TTL 自体は残存アイテムの整理のためだけにある。

## `status#<種別>#<ref>` — リソース単位の実行結果

ドメイン表現は `model.Status` である。値は最後にアクションまたはエラーが発生した時点のスナップショットであり、ライブ状態ではない。属性を、以下に示す。

| 属性 | 型 | 意味 |
|---|---|---|
| `pk` | S | `STATUS#<リソース ID>` |
| `sk` | S | `CURRENT` |
| `observed_state` | S | 直近のアクション時に観測した実状態 |
| `last_action` | S | 直近に実行したアクション |
| `last_desired` | S | 直近のアクションが対象とした望ましい状態 |
| `last_action_at` | S | 上記の時刻(RFC3339) |
| `last_error` | S | 直近のエラー内容 |
| `last_error_at` | S | 上記の時刻(RFC3339) |
| `transitioning_since` | S | 継続中の遷移の開始時刻(RFC3339)。他の属性と違いスナップショットではなく、遷移が解消した時点で消える |
| `pending_operation_id` | S | AWS 操作の前に記録する操作 ID。空でなければ完了確認待ち |
| `pending_action` / `pending_desired` / `pending_observed` | S | 未完了操作のアクション、目的、操作前の観測値 |
| `pending_started_at` | S | 未完了操作の開始時刻(RFC3339) |
| `notification_pending` | S | 通知の再送待ちにある操作 ID |

Status は履歴ではなく最新値 1 件だけを保持する。AWS 操作の前に pending 属性を条件付きで保存し、操作後は同じ操作 ID を条件に last 属性と通知待ちへ進める。途中で Lambda が終了した場合、次回は AWS の観測結果から完了を確定し、同じ操作を再実行しない。通知は `operation_id` を含み、Publish 成功後に通知待ちを解除するため、解除前に終了した場合は同じ ID で再送される。

`<種別>#<ref>` は `model.Resource.ID()` が生成する識別子であり、`internal/aws/tagging` が ARN から導出する。種別ごとの `ref` の形式を、以下に示す。

| 種別 | `ref` の形式 | `pk` の例 |
|---|---|---|
| `rds-instance` | DB インスタンス識別子 | `STATUS#rds-instance#dev-db` |
| `rds-cluster` | クラスター識別子 | `STATUS#rds-cluster#dev-cluster` |
| `ecs-service` | `<クラスター名>/<サービス名>` | `STATUS#ecs-service#dev-cluster/api` |
| `ec2-instance` | インスタンス ID | `STATUS#ec2-instance#i-0abc123` |

### 書き込み

書き込みは `UpdateItem` の `SET` で行う。`PutItem` による全置換ではないため、一部の属性だけを更新して他を残す部分更新ができる。

更新する属性は `state.StatusPatch` で指定する。各フィールドはポインタであり、`nil` は該当属性に触らないこと、`state.Set("")` は該当属性を空にすることを意味する。属性名を知るのは `internal/state` だけであり、アプリケーション層が DynamoDB の属性名を文字列で組み立てる経路は存在しない。

### 削除

セレクタに一致しなくなったリソースのアイテムは自動削除されない。残存しても動作に影響しない。削除を行うのは診断経由の孤立レコード削除のみであり、その判定条件と削除範囲は [overview.md](overview.md) にある。

## `status#group#<名前>` — グループ単位の実行結果

属性形は `status#<種別>#<ref>` と同一である。対象は個々のリソースではなくグループの処理そのものであり、cron・timezone の不正、検出の失敗、セレクタの重複といった、そのグループの設定に由来する失敗を記録する。

`"group"` はリソースタイプの定数として使われないため、実リソースの `pk` 空間と衝突しない。グループの削除時に `group#`・`override#` とあわせて削除され、リソース単位の `status#` は対象外である。

セレクタの重複は、リソース側ではなくこちらに記録する。共有アイテムへ書いた場合、そのリソースを所有するグループによるエラークリアと、無視される側のグループによるエラー記録とが、同一アイテムに対して毎サイクル交互に発生し、通知が発振するためである。

## Reconcile リース

`LOCK` / `RECONCILE` は、呼び出し全体の多重実行を防ぐリースである。`owner` に呼び出し固有 ID、`expires_at` に有効期限を保持する。取得は「アイテムが無い、または期限切れ」を条件とする `UpdateItem`、解除は owner の一致を条件とする `DeleteItem` で行う。取得できない呼び出しは設定の読み取りや AWS API の呼び出しを行わず、成功として終了する。

## 読み書きマトリクス

アイテム種別ごとの、コンポーネントからの読み書きの可否を、以下にまとめる。

| アイテム | reconciler | `cheapskate-cli` / Web コンソール |
|---|---|---|
| グループ設定 | 読み取りのみ | 読み書き、グループ削除時に削除 |
| Override | 読み取りのみ | 読み書き、明示的な解除とグループ削除時に削除 |
| リソース Status | 読み書き | 読み取りのみ、孤立レコードのみ削除 |
| グループ Status | 読み書き | 読み取りのみ、グループ削除時と孤立レコードのみ削除 |
| Reconcile リース | 読み書きと削除 | アクセスしない |

この分離は 3 段構えで担保する。各段の担保内容を、以下に示す。

| 段 | 担保の内容 |
|---|---|
| 型 | `internal/state` への窓口は、利用側が必要分だけを宣言したインターフェースである。`reconcile.Store` には `PutGroup`/`PutOverride` が無く、`groups.Store` と `doctor.Store` には `UpdateStatus` が無い |
| コード | 上記の結果として、reconciler から設定を書く経路も、CLI と Web コンソールから status を書く経路もコンパイルできない |
| IAM | reconciler の `UpdateItem` を `STATUS#*` と `LOCK`、`DeleteItem` を `LOCK` に限定する。CLI と Web コンソールは `CONFIG` のみを書き換えられる |

## アクセスパターン

通常の一覧と reconcile は、`pk=CONFIG` への一貫性の強い `Query` と、必要な Status だけを 100 キー単位で読む一貫性の強い `BatchGetItem` を使う。テーブル全体の `Scan` は `doctor` に限る。グループ 1 件の表示は設定、override、グループ Status の 3 キーを 1 回の `BatchGetItem` で読む。

グループ設定は属性単位の `UpdateItem` で変更する。異なる属性への同時変更は保持され、同じ属性への同時変更は最後の書き込みが有効となる。読み取り後の全置換は行わない。
