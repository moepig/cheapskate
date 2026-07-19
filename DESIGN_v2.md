# DESIGN v2: 現行実装レビューと改修方針

> **データモデルは v3 でタグ中心に刷新済み。** 本書が前提とする「1 リソース = 1 `config#` アイテム」は現行実装と一致しない。現行スキーマと意味論上の決定事項は [DESIGN_v3.md](DESIGN_v3.md) を参照。

[DESIGN.md](DESIGN.md) の要件・決定事項に対して現行実装を突き合わせ、(A) 不足しているテスト・実装、(B) 異常系考慮の問題、(C) 改善すべき設計、をそれぞれ対応方針付きで列挙する。項番は優先度順ではなく分類順。各節末尾に推奨着手順を示す。

## A. 要件に対して不足するテスト・不足する実装

### A-1. コンテナスモークテストが存在しない(実装不足)

DESIGN.md のテスト戦略表は「コンテナスモーク: RIE に HTTP POST、Floci に接続」を掲げるが、Makefile にターゲットが無く、スクリプトも存在しない。イメージが Lambda ランタイムとして起動・応答することを検証する手段が現状 CI に無い。

**対応方針**: `make smoke` を追加する。`docker run` でイメージを起動(`AWS_ENDPOINT_URL` を Floci コンテナに向け、`STATE_TABLE_NAME` はテスト用テーブル)、`curl -XPOST localhost:9000/2015-03-31/functions/function/invocations -d '{}'` で Summary JSON が返ることを検証するシェルスクリプト(または `//go:build smoke` の Go テスト)を置く。webconsole エントリポイント(`ImageConfig.EntryPoint` 切替)の起動確認も同スクリプトでカバーする。

### A-2. RDS ターゲット(internal/target/rds.go)の単体テストが皆無

DESIGN.md が明記する防御「Describe が空リストを返した場合も not-found として扱う」(rds.go:39, 71)、`DBInstanceNotFoundFault` / `DBClusterNotFoundFault` の not-found 変換、`rdsObservation` の状態マッピング(available/stopped/その他→transitioning)がどのテストでも直接検証されていない。ECS には ecs_test.go があるのに RDS だけ空白で、統合テストも Stop/Start をスパイ置換するため実質 Describe の正常系しか通らない。

**対応方針**: `rds_test.go` を追加。`RdsAPI` のフェイクで (1) 状態文字列ごとの Observation マッピングのテーブルテスト、(2) NotFoundFault → StateNotFound、(3) 空リスト → StateNotFound、(4) その他のエラーは素通し、をインスタンス・クラスター両方で検証する。

### A-3. SnsNotifier の単体テストが無い

`TopicArn` 空での no-op、件名 99 文字切詰め(notifier.go:29-31)が未検証。切詰めが byte 単位のため、マルチバイト文字を含む resource_id で UTF-8 の途中で切れる潜在問題もテストが無いことで気づけない(SNS の Subject は ASCII 前提でもあり、B-7 参照)。

**対応方針**: `notifier_test.go` を追加し、no-op・件名切詰め・payload の JSON 化を検証。B-7 の修正(rune 単位切詰め + ASCII サニタイズ)とセットで行う。

### A-4. ops パッケージの仕様がテストで固定されていない

`internal/ops` を直接叩く単体テストが無く、cheapskate-cli 統合テストと webconsole テスト経由の部分カバーのみ。特に以下の明文仕様が未検証:

- **「pin は旧 cron 設定を保持し schedule で復帰可」**(DESIGN.md の CLI 節、ops.go:81-86)。pin → schedule で cron が戻るシナリオはどのテストにも無い
- `Schedule` が既存 config の `RestoreCount` を破棄する非対称動作(B-10 のバグでもある)
- `Remove` が 3 プレフィックス全部を消すこと

**対応方針**: dynafake を使った `ops_test.go` を追加し、pin↔schedule の往復での属性保持、override の登録前拒否、Remove の全消しをユニットで固定する。統合テストは「実 DynamoDB での動作確認」に役割を縮小する。

### A-5. cron 同時刻タイ → stop の決定事項が未検証

決定事項サマリに「同時刻タイは stop(フェイルセーフ)」とあるが(schedule.go:55-59)、start_cron と stop_cron が同一時刻に発火するケースのテストが無い。既存テストの「exactly at stop」は片側発火のみ。

**対応方針**: schedule_test.go に start/stop 両方が同時刻(例: 両方 `0 9 * * *`)で stopped になるケースを追加。あわせて override 期限ちょうど(`ExpiresAt == now.Unix()` は失効扱い、schedule.go:18 の `>`)の境界テストも追加する。

### A-6. ListConfigs のページネーション経路が未検証

store.go:41-60 の `LastEvaluatedKey` ループは、dynafake がページングを実装しない(常に 1 ページ)ため単体で通らず、統合テストもページを跨ぐ件数を投入しない。Scan 1MB 超えで初めて動くコードが本番まで未実行になる。

**対応方針**: dynafake の Scan に「1 ページ N 件 + LastEvaluatedKey 返却」を実装し(C-5)、複数ページを結合するテストを追加する。

### A-7. エラー報告フォールバック経路のテストが無い

reconcile.go:209-222(エラー時の PutStatus 失敗、Notifier.Publish 失敗のログ縮退)は、dynafake / fakeNotifier にエラー注入機構が無いため一度も実行されない。「per-resource でエラーを閉じ込める」規約の最後の砦が未検証。

**対応方針**: dynafake に操作別のエラー注入(例: `FailOn(op, pk)`)、fakeNotifier に `publishErr` を追加し、(1) アクション後の PutStatus 失敗、(2) Publish 失敗、(3) エラー記録自体の失敗、でも Run が panic せず他リソースが処理されることをテストする。B-4 の仕様変更とセットで行う。

### A-8. webconsole のカバレッジ不足

schedule / disable の成功経路、`BASE_PATH` 付きリダイレクト(`base` が Location に乗ること)、detail テンプレートに override/status が描画されることが未検証。

**対応方針**: 既存の webconsole_test.go に、`New(store, "/console", loc)` でのリダイレクト先検証、schedule 成功→config 書込検証、disable 成功のケースを追加する。

### A-9. DST 跨ぎのスケジュール解決テストが無い

タイムゾーン対応(tzdata 埋め込み)を要件にしながら、DST 切替日に PrevTickBefore が期待通り動くことのテストが無い。Asia/Tokyo のみのテストでは DST バグを検出できない。

**対応方針**: `America/New_York` 等で、春の欠落時刻(2:30 が存在しない日)・秋の重複時刻を跨ぐ start/stop 解決のケースを schedule_test.go に追加する。gronx の挙動をそのまま仕様として固定する(結果が不自然なら DESIGN に制約として明記)。

## B. 異常系考慮の問題

### B-1. ECS 停止のクラッシュ安全性: saved 値の書き込みが AWS 変更より後【最重要】

`ReconcileOne` は `act()`(AWS 状態の変更)→ `PutStatus`(saved 値の永続化)の順で実行する(reconcile.go:186-198)。ECS の Stop は scaling を 0/0 に変更し desiredCount を 0 にした**後**でしか saved_desired_count / saved_scaling_min/max が DynamoDB に書かれない。UpdateService と PutStatus の間で Lambda がクラッシュ・タイムアウトすると:

- 次周期は desiredCount=0 → observed stopped → 収束扱いで再試行されない
- 復帰時、`restore_count` 未設定なら desiredCount は 1 にフォールバック
- scaling は `SavedScalingMin == nil` のため**復元されず 0/0 のまま永久放置**(ecs.go:100-108)

**対応方針**: write-ahead に変更する。Target インターフェースを「(1) 変更前に保存すべき属性を返す `PrepareStop`、(2) 実際の変更 `Stop`」に分離するか、簡易には Stop 内で AWS 変更前に返す attrs を確定し、reconcile 側で **PutStatus → act の順**に入れ替える(アクション失敗時は last_error が上書きするので保存済み saved 値が残っても害は無い。saved 値は「停止前の姿」であり、変更前に書くのが意味的にも正しい)。last_action / last_action_at のみ act 成功後に書く 2 段書きにする。

### B-2. saved_scaling が 0/0 で上書きされ復元値が失われる

B-1 の部分停止後や手動介入後(scaling 0/0 のまま誰かが desiredCount を上げた状態)で再度 Stop が走ると、現在の scalable target(0/0)を saved_scaling_min/max として保存してしまい(ecs.go:78-88)、本来の復元値が永久に失われる。

**対応方針**: Stop 時に取得した min/max が 0/0 の場合は saved_scaling_* を上書きしない(既存の saved 値を保持)。「0/0 は cheapskate 自身が書いた値」とみなせるため安全。あわせて saved_desired_count も 0 なら既存値を保持する(現行は `restoreCount` 側で 0 を無視しているが、保存段階で守る方が一貫する)。

### B-3. 恒久エラーの SNS 通知洪水

not-found(リソース削除済みで config が残存)、AccessDenied(権限外タイプの登録 — DESIGN が明示的に想定するケース)等の恒久エラーは、5 分周期で毎回 SNS 通知される(reconcile.go:216-221)。1 リソースで 288 通/日となり、通知の実用性が失われる。

**対応方針**: 通知前に既存 status を読み、`last_error` が同一メッセージなら通知をスキップする(status への記録は毎回でよい。エラーメッセージが変わったら通知)。回復時(エラー→アクション成功 or 収束)には「recovered」を 1 回通知し、`last_error` をクリアする(B-11)。「初回 + 変化時のみ通知」を DESIGN の規約に追記する。

### B-4. 通知失敗がアクション成功を「エラー」として記録する

アクション成功後の `Notifier.Publish` の戻り値がそのままクロージャのエラーになる(reconcile.go:203-206)。SNS が落ちていると、(1) 成功したアクションが Summary.Errors に載り、(2) status に last_error が書かれ、(3) さらに同じ壊れた Notifier で error 通知を再試行する。「通知はベストエフォート、リコンサイル結果とは別物」という切り分けができていない。

**対応方針**: アクション成功後の Publish 失敗はログ(`notify-failed`)に留めて err として返さない。通知失敗を last_error に混ぜない。A-7 のテストで固定する。

### B-5. 不正な override アイテム 1 件で list / index が全滅する

`ops.List` は各行の `GetOverride` エラー(desired 不正など、手書き・Terraform 起因で起こりうる)で全体を即 return する(ops.go:33-36)。cheapskate-cli list と webconsole のトップページが 1 件の不正データで丸ごと使えなくなり、原因のリソースを特定する手段も失われる。

**対応方針**: `Row` にエラーフィールドを追加し、行単位で保持して一覧表示を継続する(cheapskate-cli はその行に error を表示、webconsole は行をエラースタイルで描画)。reconcile 側は既に per-resource で閉じ込めているので対象外。

### B-6. disabled + override の組合せが無言で無効

`ReconcileOne` は override を読む前に mode=disabled でスキップし(reconcile.go:139-142)、`ResolveDesired` も disabled を最優先で返す(schedule.go:15-17)。一方 cheapskate-cli / webconsole は disabled リソースへの override 登録を警告なく受け付ける。「override が最優先」という DESIGN の記述と実装が食い違い、利用者は override が効かない理由に気づけない。

**対応方針**: 仕様としては「disabled は override より強い(完全な管理停止)」を採用し、DESIGN と cheapskate-cli ヘルプに明記する。その上で `ops.SetOverride` は対象 config が mode=disabled のとき拒否(またはエラーメッセージ付き警告)する。

### B-7. SNS Subject の制約違反リスク

SNS の Subject は ASCII 100 文字までで、違反すると Publish 自体が `InvalidParameter` で失敗する。現行の 99 byte 切詰め(notifier.go:29-31)はマルチバイトの途中で切れる可能性があり、非 ASCII を含む resource_id(ECS サービス名等)では通知が常に失敗 → B-4 と複合してアクションがエラー扱いになる。

**対応方針**: Subject を ASCII にサニタイズ(非 ASCII は `?` 置換)した上で 100 文字未満に丸める。resource_id 全体は payload(Message)に既に入っているので情報欠落はない。A-3 のテストで固定する。

### B-8. webconsole にクリックジャッキング対策が無い

CSP に `frame-ancestors` が無く、`X-Frame-Options` も無い(webconsole.go:69-77)。`sameOrigin` は iframe 内からの正規フォーム送信を same-origin として通すため、許可 CIDR 内の操作者を外部サイトの透明 iframe で誘導すれば remove / pin 操作を実行させられる。

**対応方針**: CSP に `frame-ancestors 'none'` を追加し、後方互換に `X-Frame-Options: DENY` も併せて送る。webconsole_test.go にヘッダ検証を追加する。

### B-9. Schedule が既存 config の restore_count を黙って破棄する

`ops.Schedule` は既存アイテムを読まず新規アイテムで置き換えるため、`-restore-count` を付けずに cron だけ変更した再実行で restore_count が消える(ops.go:120-131)。`Pin` は既存値を保持する設計(ops.go:81-86)と非対称で、ECS の復帰台数が意図せず 1 に落ちる実害がある。

**対応方針**: `Pin` と同様に既存 config を読み、`RestoreCount`(と `Desired` — schedule 下では不活性だが pin 復帰用)を引き継ぐ。明示的に消したい場合のために `-restore-count 0` を「クリア」と定義するか、`cheapskate-cli remove` → 再登録に誘導する。A-4 のテストで固定する。

### B-10. 収束済みでも observed_state が status に反映されない

差分なしの周期は一切書き込まない設計(意図的)だが、その結果 `cheapskate-cli list` の OBSERVED 列は「最後にアクションした時点」の状態のまま古くなる。手動で起動された DB が transitioning → running になっても status は stopped 直前の値を示し続け、監査用途(DESIGN が status の目的とするもの)に対して誤解を招く。

**対応方針**: 書き込み抑制の設計は維持しつつ、表示側で「observed_state は last_action 時点のスナップショット」であることを cheapskate-cli list のヘッダ(例: `OBSERVED(AT LAST ACTION)`)と docs に明記する。リアルタイム性が必要になった場合のみ「observed が前回記録値から変化した時だけ書く」への拡張を検討する(書き込み頻度は転移時のみで増分は小さい)。

## C. 改善すべき設計

### C-1. ops.List の N+1 アクセス

config n 件に対し GetOverride + GetStatus で 2n 回の GetItem を発行する(ops.go:25-44)。数十リソース規模でも動くが、webconsole のトップページ表示が線形に遅くなり、DynamoDB 呼び出しも無駄。

**対応方針**: テーブル全体を 1 回の Scan(ページネーション付き)で取得し、prefix ごとにメモリ上で結合する `store.ScanAll` を追加して List をその上に載せ替える。テーブルには 3 種のアイテムしか無いためフィルタ不要。B-5 の行単位エラー保持もこの結合処理に自然に収まる。

### C-2. Stop/Start の戻り値 `map[string]any` が暗黙契約

Target が返す status 属性名(`saved_desired_count` 等)が文字列リテラルで散在し、model.Status のフィールドとの対応がコンパイラで守られない。タイポが実行時までバグとして潜伏する。

**対応方針**: 戻り値を `*model.SavedState`(struct)にし、store 側で属性名へマッピングする関数を 1 箇所に置く。B-1 の Prepare/act 分離と同時に行うと変更が一度で済む。

### C-3. ReconcileOne の単一クロージャ肥大

resolve → describe → act → persist → notify が 1 つの無名関数に詰まっており(reconcile.go:134-207)、B-1/B-3/B-4 の修正でさらに分岐が増える。

**対応方針**: 「desired 解決」「観測と収束判定」「アクション実行と記録」「通知ポリシー」を私有関数に分割し、Result を組み立てる骨格だけを ReconcileOne に残す。挙動変更なしのリファクタとして B 系修正の前に実施する。

### C-4. 全量リコンサイルのトリガが「aws.rds 以外の任意 JSON」

`Run` は source が `aws.rds` でなければ何でも全量リコンサイルする(reconcile.go:72-82)。誤配線されたイベント(別ルール、手動テスト invoke のミス)が黙って全量実行になり、設定ミスに気づけない。

**対応方針**: 挙動は互換のまま、`source` が空でない未知イベントのときは `unexpected-event-source` を警告ログに出す。将来的に Scheduler 側のペイロードを `{"mode":"full"}` に固定し、空 `{}` と未知 source を deprecated 扱いにする移行パスを docs に書く。

### C-5. dynafake の表現力不足がテスト空白の根因

エラー注入なし・ページングなしのため、A-6 / A-7 のテストが書けない構造になっている。

**対応方針**: dynafake に (1) 操作・pk 指定のエラー注入、(2) Scan のページサイズ設定、を追加する。フェイクの複雑化を避けるため、条件式など store が発行しない機能は今後も実装しない方針を package コメントに明記する。

### C-6. cheapskate-cli の FlagSet が ExitOnError

サブコマンドの flag パース失敗で `run()` がテスト不能な `os.Exit` に到達する(main.go:54, 185, 224)。統合テストが検証しているのは「パースを通ったあとの検証エラー」だけで、フラグ誤用のエラーメッセージは無検証。

**対応方針**: `flag.ContinueOnError` に変更してエラーを `run()` の戻り値に載せ、usage 表示は main 側で行う。cheapskate-cli のフラグ誤用ケースを統合テストに追加できるようになる。

## 推奨着手順

1. **B-1 / B-2**(ECS saved 値の write-ahead 化)— 実害が復元不能なため最優先。C-2/C-3 のリファクタを同時に行う
2. **B-4 / B-3 / B-11 相当のクリア処理**(通知とエラー記録の分離・抑制)+ **A-7**(エラー注入テスト)と **C-5**(dynafake 拡張)
3. **B-8**(frame-ancestors)と **B-7 / A-3**(SNS Subject)— 小さく独立、即日可能
4. **B-5 / C-1**(list の行単位エラー + Scan 一括化)、**B-9 / A-4**(ops の属性保持とテスト)、**B-6**(disabled×override の仕様明文化)
5. **A-1**(スモークテスト)、**A-2 / A-5 / A-6 / A-8 / A-9**(テスト空白の充填)、**C-4 / C-6**(トリガ明示化・CLI パース)
