# DESIGN v3: タグ中心データモデルへの刷新

[DESIGN.md](DESIGN.md) と [DESIGN_v2.md](DESIGN_v2.md) が前提とする「1 リソース = 1 `config#` アイテム」というデータモデルを、「1 タグ = 複数リソース」を基本単位とするモデルに置き換えた。この文書は変更後のスキーマと、実装時に確定させた意味論上の決定事項をまとめる。実装の網羅的な説明は各パッケージのコード自体を参照し、ここでは「なぜこうしたか」に絞る。

## 動機

運用上、RDS/ECS リソースは個別にではなく「環境」や「チーム」単位でまとめて起動・停止したいことが大半だった。旧モデルでは pin/schedule/override をリソースごとに設定する必要があり、環境を構成する N 個のリソースに同じ設定を N 回書く(そして N 回ズレる)運用になっていた。タグを設定の単位にすることで、設定は 1 か所、リソースはそこに参加するだけ、という形にした。

## スキーマ

テーブル定義(パーティションキー `pk` のみ、TTL は `expires_at`)は変更なし。アイテムは 4 種類:

```
tag#<name>            mode / desired / start_cron / stop_cron / timezone
member#<resourceID>   tag / type / restore_count(ecs-service のみ)
override#<name>       desired / expires_at(TTL) — タグ名がキー
status#<resourceID>   observed_state / last_action(_at) / last_error(_at) / saved_*（リソース単位のまま）
```

`internal/model/model.go`: `TagItem`/`TagConfig`(`ParseTag`)、`MemberItem`/`Member`(`ParseMember`)。`ConfigItem`/`Config`/`ParseConfig`/`ConfigPrefix` は削除した(過去データとの互換は取らない — 後述)。

### なぜ `member#<resourceID>` + `tag` 属性なのか

候補は 3 つあった: (1) `member#<resourceID>` に `tag` 属性を持たせる(採用)、(2) `member#<tag>#<resourceID>`、(3) タグアイテムに文字列集合でメンバー一覧を持たせる。

- **1 リソース 1 タグの原子性**: (1) なら「そのリソースの member アイテムが既に存在するか」を `PutItem` + `ConditionExpression: attribute_not_exists(pk)` 一発で判定・強制できる(`store.CreateMember`, `store.ErrMemberExists`)。(2) は「このリソースは今どのタグにいるか」を知るのに別途逆引きが要る。(3) はタグアイテムへの read-modify-write になり、CLI と Web コンソールからの同時操作でロストアップデートが起きうる。
- **RDS イベントの高速経路**: EventBridge の RDS 自動起動イベントは resource_id しか運んでこない。(1) なら `GetMember(resourceID)` 一発でタグが分かる(`reconcile.runForRdsEvent`)。
- **一覧表示**: タグ→メンバーの集約は `store.ScanAll` の 1 回の Scan の中でグルーピングすれば足りる(件数は数十のオーダーを想定しており、full-Scan 方式は据え置き)。

### `restore_count` はメンバー属性

タグ属性ではなくメンバー属性にした。理由は単純で、1 つのタグに複数の ECS サービスが入り得る以上、「起動時の desiredCount」はサービスごとに違う値になるのが自然だからである。タグ属性にすると「このタグの ECS サービスは全部同じ desiredCount で起動する」という誤った制約を持ち込んでしまう。`add -restore-count N`(ecs のみ)で設定し、同一タグへの再 `add` は上書き(値省略時は既存値を保持 — 旧 B-9 と同じ思想)。

### `override#` はタグ名キーに変更

override はタグの構成要素(pin/schedule と同格の「一時的な上書き」)なので、タグと同じ粒度で持つのが自然。これにより override 系のコードパス(`store.GetOverride`/`PutOverride`, `schedule.ResolveDesired`)はキーがリソース ID からタグ名に変わっただけで、TTL による失効ロジックなどはそのまま。

## 意味論上の決定事項

### D-1. `add` はタグを暗黙作成するが、`pin`/`schedule`/`disable`/`override` はしない

`ops.Add` は対象タグが存在しなければ `mode=disabled` で作成する(`add` → `pin`/`schedule` という操作順が自然なため)。一方 `ops.Pin`/`Schedule`/`Disable`/`SetOverride` は既存タグを要求し、無ければ `tag %q not found (create it by adding a member: ...)` を返す。タグ名の打ち間違いが「何もしないタグを黙って作る」事故にならないようにするための非対称。

### D-2. メンバーは即座に継承する

タグの設定はタグアイテムに 1 つだけ存在し、reconciler は「タグの desired state を 1 回解決 → 各メンバーに適用」という順で処理する(`reconcile.ReconcileTag`)。したがって、既にスケジュール済み・pin 済みのタグに後から `add` したメンバーは、追加の設定操作なしに次の reconcile サイクルから対象になる。これは実装上の副産物ではなく要件そのもの(`reconcile_test.go` の `TestAddAfterScheduleAppliesOnNextReconcile` で固定)。

### D-3. タグレベルのエラーはメンバーごとに記録・通知する

不正な cron やタイムゾーンなど、タグの設定自体が壊れている場合、それはタグの全メンバーに影響する 1 つの原因である。しかし記録・通知の主体は変えず、旧来のメンバー単位の `recordFailure`/notify-once dedup(`status#` の `last_error` 比較)をそのまま流用した。結果として、1 つのタグ設定ミスは「メンバー数ぶんの初回通知」を生むが、以後は各メンバーごとに dedup されて沈黙する。タグ単位で 1 通に集約する経路を別に作るよりも、既存の実績あるメンバー単位の障害隔離・通知機構をそのまま使う方がバグの少ない選択だと判断した。

### D-4. RDS イベントのスコープはメンバー単位のまま

EventBridge の RDS 自動起動イベントは、そのリソース 1 件だけを reconcile する(同じタグの他メンバーには一切触れない)という DESIGN.md 以来の性質を保持した。実装は `GetMember` → `GetTag` → `GetOverride` で 1 メンバー分の合成 `TagRow` を作り、通常のタグ reconcile ループ(`ReconcileTag`)に 1 件だけ流し込む形。

### D-5. 確認コマンドはタグ→リソースを解決して見せる

`cheapskate-cli list`/`show --tag` と Web コンソールは、タグの設定だけでなく「実際にそのタグが適用されているメンバー」を状態つきで解決して表示する(`ops.List`/`ops.Get` が返す `TagRow.Members`)。タグの設定を見ただけでは「どのリソースが今それに従っているか」が分からないため、これは確認コマンドの必須要件とした。

### D-6. 破壊的変更、移行なし

旧 `config#` アイテムは新しい `ScanAll` の prefix dispatch から単に無視される(未知の prefix は捨てる)。データ移行コードは書いていない。既存テーブルを使い回す場合は、テーブルを作り直すか、`config#`/古い形式の `override#<resourceID>` アイテムを掃除してから使うこと(運用は [usage/setup.md](docs/en/usage/setup.md) 参照)。

## 主要な型・関数の対応表

| 旧 | 新 |
|---|---|
| `model.ConfigItem`/`Config`/`ParseConfig` | `model.TagItem`/`TagConfig`/`ParseTag` + `model.MemberItem`/`Member`/`ParseMember` |
| `store.ListConfigs`/`GetConfig`/`PutConfig` | `store.GetTag`/`PutTag`, `store.GetMember`/`PutMember`/`CreateMember`/`ListMembers` |
| `store.ScanRow`(resourceID 単位) | `store.TagRow`(タグ単位、`Members []MemberRow`) |
| `ops.Row`/`ops.Remove` | `ops.TagRow`/`ops.RemoveMember`/`ops.RemoveTag`、`ops.Add`、`ops.AssembleResourceID` |
| `reconcile.ReconcileOne` | `reconcile.ReconcileTag`(タグ単位で desired を解決し、各メンバーに適用) |
| `csctl <command> <resource-id>` | `cheapskate-cli <command> --tag NAME [--type ... --name/--cluster/--service ...]` |

詳細な実装はコード自体(特に `internal/store/store.go` の `ScanAll`、`internal/ops/ops.go`、`internal/reconcile/reconcile.go` の `ReconcileTag`)を正とする。
