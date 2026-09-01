# DynamoDB テーブル構造

state を保持する DynamoDB テーブル 1 つのキー配置とアイテム形式。スキーマ・キー配置・アイテムの Go 表現はすべて `internal/state` に閉じており、`internal/core/model` はドメイン型のみを持ち保存形式を知らない。

## テーブル定義

テーブルに要求する定義を、以下に示す。

| 項目 | 値 |
|---|---|
| パーティションキー | `pk`(String) |
| ソートキー | `sk`(String) |
| GSI / LSI | なし |
| TTL | 属性 `expires_at`(Number、epoch 秒)。override、Status、reconcile リースが持つ |

## アイテム種別一覧

保持するアイテムは 5 種類である。キーの値が表す内容と、書き手・読み手を、以下にまとめる。

| アイテム | `pk` の値と意味 | `sk` の値と意味 | 書き手 | 読み手 |
|---|---|---|---|---|
| グループ設定 | `CONFIG` — グループに対する設定入力を集約するパーティション | `GROUP#<名前>` — 指定したグループの恒久設定 | `cheapskate-cli` / Web コンソール | reconciler、CLI、Web コンソール |
| Override | `CONFIG` — グループに対する設定入力を集約するパーティション | `OVERRIDE#<名前>` — 指定したグループの期限付き上書き | 同上 | 同上 |
| Status(リソース単位) | `STATUS#<種別>#<ref>` — 指定した AWS リソースの実行結果 | `CURRENT` — そのリソースの最新 Status | reconciler | CLI、Web コンソール |
| Status(グループ単位) | `STATUS#group#<名前>` — 指定したグループの処理結果 | `CURRENT` — そのグループの最新 Status | reconciler | CLI、Web コンソール |
| Reconcile リース | `LOCK` — reconcile の排他制御用パーティション | `RECONCILE` — 全体 reconcile のグローバルリース | reconciler、`doctor --prune` | 同左 |

キーの組み合わせは [`itemKey`](../../../internal/state/items.go) が表す。`STATUS#<種別>#<ref>` の `<種別>` は [`model.ResourceType`](../../../internal/core/model/resource.go) が定義し、`<ref>` の形式はリソース単位の実行結果の節に示す。キーの固定値とプレフィックスは大文字・小文字を区別する。

## `CONFIG` / `GROUP#<名前>` — グループ設定

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

## `CONFIG` / `OVERRIDE#<名前>` — 期限付きの上書き

ドメイン表現は `model.Override` である。属性を、以下に示す。

| 属性 | 型 | 意味 |
|---|---|---|
| `pk` | S | `CONFIG` |
| `sk` | S | `OVERRIDE#<名前>` |
| `desired` | S | `running` \| `stopped` |
| `expires_at` | N | epoch seconds。DynamoDB TTL の対象属性 |

読み取り側が `expires_at <= now` のアイテムを期限切れとして無視する。TTL 削除は非同期であり、最大 48 時間の遅延がありうるためである。TTL 自体は残存アイテムの整理のためだけにある。

## `STATUS#<種別>#<ref>` / `CURRENT` — リソース単位の実行結果

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
| `pending_group` | S | 操作開始時にリソースを所有していたグループ名 |
| `pending_config_hash` | S | 操作開始時に有効だったグループ設定、override、解決済み desired state の SHA-256 ハッシュ |
| `pending_action` / `pending_desired` / `pending_observed` | S | 未完了操作のアクション、目的、操作前の観測値 |
| `pending_started_at` | S | 未完了操作の開始時刻(RFC3339) |
| `expires_at` | N | 最後の Status 更新時刻に `STATUS_RETENTION_DAYS` を加えた epoch 秒。DynamoDB TTL の対象属性 |

Status は履歴ではなく最新値 1 件だけを保持する。更新のたびに `expires_at` を延長し、更新されなくなった Status は保持期間後に DynamoDB TTL が削除する。AWS 操作の前に pending 属性を条件付きで保存し、操作後は同じ操作 ID を条件に last 属性へ進める。途中で Lambda が終了した場合、次回は AWS の観測結果から完了を確定し、同じ操作を再実行しない。操作中にタグ所属や設定が変わった場合も、`pending_group` を完了通知の宛先とし、現在の設定は次のサイクルから適用する。通知は Status に保持しない。永続化と通知の境界は、[Reconcile の永続化境界](../development/reconcile.md) を参照。

Status は属性ごとに復号する。監査属性の型が不正な場合も、正常な属性と復号エラーを別々に保持し、グループ設定と override の解決および AWS 操作は継続する。`pending_` で始まる属性の型が不正な場合は、AWS 操作が実行済みかを判定できないため、そのリソースの操作を停止する。復号エラーは `cheapskate-cli`、Web コンソール、および `doctor` に表示する。

`<種別>#<ref>` は `model.Resource.ID()` が生成する識別子であり、`internal/aws/tagging` が ARN から導出する。種別ごとの `ref` の形式を、以下に示す。

| 種別 | `ref` の形式 | `pk` の例 |
|---|---|---|
| `rds-instance` | DB インスタンス識別子 | `STATUS#rds-instance#dev-db` |
| `rds-cluster` | クラスター識別子 | `STATUS#rds-cluster#dev-cluster` |
| `ecs-service` | `<クラスター名>/<サービス名>` | `STATUS#ecs-service#dev-cluster/api` |
| `ec2-instance` | インスタンス ID | `STATUS#ec2-instance#i-0abc123` |

### 書き込み

書き込みは `UpdateItem` の `SET` で行う。`PutItem` による全置換ではないため、一部の属性だけを更新して他を残す部分更新ができる。

更新する属性は `state.StatusPatch` で指定する。各フィールドはポインタであり、`nil` は該当属性に触らないこと、空文字列を指すポインタは該当属性を空にすることを意味する。属性名を知るのは `internal/state` だけであり、アプリケーション層が DynamoDB の属性名を文字列で組み立てる経路は存在しない。

### 削除

セレクタに一致しなくなるなどして更新が止まったアイテムは、最後の更新から保持期間が経過すると DynamoDB TTL の削除対象になる。削除は非同期であり、期限後も最大 48 時間残る場合がある。保持期間より前に削除する場合は、[overview.md](overview.md) に示す診断経由の孤立レコード削除を用いる。`doctor --prune` は `pending_operation_id` が存在しないことを DeleteItem の条件とし、診断後に未完了操作が作成された Status を削除しない。

`expires_at` を持たない既存の Status は TTL の対象外である。reconciler が更新すると期限が設定されるが、更新されない既存の孤立 Status は `doctor --prune` で削除する。

## `STATUS#group#<名前>` / `CURRENT` — グループ単位の実行結果

属性形は `STATUS#<種別>#<ref>` / `CURRENT` と同一である。対象は個々のリソースではなくグループの処理そのものであり、cron・timezone の不正、検出の失敗、セレクタの重複といった、そのグループの設定に由来する失敗を記録する。グループ Status の復号エラーはグループ設定のエラーと区別し、リソースの reconcile を停止しない。

`"group"` はリソースタイプの定数として使われないため、実リソースの `pk` 空間と衝突しない。グループの削除時に `CONFIG` / `GROUP#<名前>`、`CONFIG` / `OVERRIDE#<名前>` とあわせて削除される。リソース単位の `STATUS#<種別>#<ref>` / `CURRENT` は対象外である。

セレクタの重複は、リソース側ではなくこちらに記録する。共有アイテムへ書いた場合、そのリソースを所有するグループによるエラークリアと、無視される側のグループによるエラー記録とが、同一アイテムに対して毎サイクル交互に発生し、通知が発振するためである。

## Reconcile リース

`LOCK` / `RECONCILE` は、reconcile の多重実行と `doctor --prune` による削除との競合を防ぐリースである。`owner` に呼び出し固有 ID、`expires_at` に有効期限を保持する。取得は「アイテムが無い、または期限切れ」を条件とする `UpdateItem`、解除は owner の一致を条件とする `DeleteItem` で行う。reconciler が取得できない場合は設定の読み取りや AWS API の呼び出しを行わず成功として終了する。`doctor --prune` が取得できない場合は削除判断に必要な Scan を開始せず、エラーを返す。

## 読み書きマトリクス

アイテム種別ごとの、コンポーネントからの読み書きの可否を、以下にまとめる。

| アイテム | reconciler | `cheapskate-cli` / Web コンソール |
|---|---|---|
| グループ設定 | 読み取りのみ | 読み書き、グループ削除時に削除 |
| Override | 読み取りのみ | 読み書き、明示的な解除とグループ削除時に削除 |
| リソース Status | 読み書き | 読み取りのみ、孤立レコードのみ削除 |
| グループ Status | 読み書き | 読み取りのみ、グループ削除時と孤立レコードのみ削除 |
| Reconcile リース | 読み書きと削除 | `doctor --prune` の実行中のみ読み書きと削除 |

この分離は 3 段構えで担保する。各段の担保内容を、以下に示す。

| 段 | 担保の内容 |
|---|---|
| 型 | `internal/state` への窓口は、利用側が必要分だけを宣言したインターフェースである。`reconcile.Store` には `PutGroup`/`PutOverride` が無く、`groups.Store` と `doctor.Store` には `UpdateStatus` が無い |
| コード | 上記の結果として、reconciler から設定を書く経路も、CLI と Web コンソールから Status の内容を書き換える経路もコンパイルできない。doctor は pending operation がない Status の条件付き削除だけを宣言する |
| IAM | reconciler の `UpdateItem` を `STATUS#*` と `LOCK`、`DeleteItem` を `LOCK` に限定する。CLI と Web コンソールは `CONFIG` の更新、`STATUS#*` の削除、および `doctor --prune` 用の `LOCK` の更新と削除に限定する |

## アクセスパターン

通常の一覧と reconcile は、`pk=CONFIG` への一貫性の強い `Query` と、必要な Status だけを 100 キー単位で読む一貫性の強い `BatchGetItem` を使う。テーブル全体の一貫性の強い `Scan` は `doctor` に限る。`doctor --prune` はリースを取得してから Scan、探索、削除を行う。グループ 1 件の表示は設定、override、グループ Status の 3 キーを 1 回の `BatchGetItem` で読む。

グループ設定は属性単位の `UpdateItem` で変更する。異なる属性への同時変更は保持され、同じ属性への同時変更は最後の書き込みが有効となる。読み取り後の全置換は行わない。
