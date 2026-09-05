# cheapskate slim 実装計画

## 概要

本ドキュメントは、[cheapskate slim 目標仕様](./spec__slim.md)を実装する順序、テスト、および完了条件を定める。目標データ形式と外部動作は目標仕様だけで管理し、本ドキュメントでは繰り返さない。

各段階は目標仕様へ直接置き換える。

## 実装順序

### 1. model と schedule

`internal/core/model` のグループ型を、schedule と override だけを表す型へ置き換える。Mode、グループ単位の Timezone、Selector、Status、revision、および pending operation の型を削除する。

検証処理を 1 か所へ集約する。検証対象はグループ名、schedule の組、5 フィールド cron、override、失効時刻、および属性間の不変条件である。

cron の構文検証後、検証時刻と `DEFAULT_TIMEZONE` を使用し、`gronx.PrevTickBefore` と `gronx.NextTickAfter` を `inclRefTime=true` で呼ぶ。どちらかの発火時刻を取得できない cron は保存時と読み取り時の双方で拒否する。

`internal/core/schedule` は override の優先判定と start / stop cron の比較だけを処理する。全呼び出しで検証済みの `time.Location` を受け取る。

この段階の完了条件は次のとおりである。

- 6 フィールド cron と 7 フィールド cron を拒否する。
- `0 0 31 2 *` を拒否し、`0 0 29 2 *` を受理する。
- 期限付き override と schedule の組を型の検証で強制する。
- `disabled` を AWS の DesiredState に含めない。

### 2. state

`internal/state` が保存するデータを `CONFIG / GROUP#<name>` のグループアイテムだけにする。グループ item は属性 allowlist によって厳密に復号する。

汎用の save と patch 型を削除し、schedule、override、clear-override、および remove に対応する state 操作を定義する。作成には条件付き `PutItem`、既存アイテムの変更には対象属性だけを変更する条件付き `UpdateItem`、削除には条件付き `DeleteItem` を使用する。`UpdateItem` の条件でアイテムの存在を検査し、削除後の upsert を防ぐ。

Status、BatchGetItem、Scan、リース、TTL、Status retention、および Status 用 backoff を削除する。

この段階の競合テストを、以下に示す。

- 削除後の stale update が item を再作成しない。
- stale clear-override が key だけの item を作成しない。
- 期限付き override の保存と schedule 変更が不正な item を作成しない。
- schedule と override の同時変更が互いの属性を失わない。
- 同じ属性への同時変更は競合を検出せず、DynamoDB で後に適用された値が残る。
- 未知属性と属性名の入力ミスを拒否する。

### 3. groups application

`internal/app/groups` を list、show、schedule、override、clear-override、および remove に限定する。selector、pin、unpin、disable、および Status 削除を除去する。

すべての設定書き込みで、強い整合性の読み取り、全体検証、および操作別の条件付き書き込みを同じ application service で実行する。CLI と Web コンソールから個別の書き込み手順を持たない。

作成と既存更新の前提が書き込み時に変わった場合は競合を呼び出し側へ返し、自動再試行しない。同名グループの remove と再作成を別の設定変更と同時に行う使い方はサポートしない。

### 4. resource discovery

`internal/aws/tagging` の Discoverer を、グループ単位の selector ではなく固定 group tag の全体探索へ変更する。1 系列の `GetResources` で全 page を取得してから、ARN をキーとする map を application 層へ返す。

map には各 ARN の最後の観測値を保持し、各 ARN を 1 サイクルに 1 回だけ処理する。group tag の値で、先に一括取得した設定 map へ関連付ける。1 group ずつ検索する実装、所有権競合の判定、および既存 claims 型は削除する。

探索テストに、全 page の取得、page 間で同じ ARN の観測値が更新される場合、未知 group、および空 group を追加する。

### 5. resource adapters

RDS instance adapter は Describe 結果の cluster 所属と engine を検査する。Aurora member と RDS Custom には Start / Stop を送らず、対象 ARN を含むエラーを返す。

RDS cluster adapter は Aurora engine だけを受け付ける。Multi-AZ DB cluster などの非 Aurora cluster はエラーにする。

ECS の desired count、scaling min、および scaling max を共通処理で解析する。リソース探索後、変更 API の呼び出し前に 3 値を検証する。Start と Stop で別の検証規則を持たない。所属タグの固定化によって ECS 設定タグを削除しない。

`port.Target.Stop` の引数をリソースの ref から `model.Resource` へ変更し、探索で取得したタグを Stop へ渡す。ECS の Start と Stop は同じタグ解析処理を呼ぶ。EC2 と RDS の Stop は `Resource.Ref` だけを使用する。ECS 専用の検証インターフェースは追加しない。

ECS adapter は既存の `DescribeServices` 結果から scheduling strategy を検査し、`REPLICA` 以外を変更対象から除外する。追加の Describe API は導入しない。

停止処理は scalable target の有無で分岐する。target がある場合は `RegisterScalableTarget(0, 0)` だけを呼び、`UpdateService` と rollback を削除する。target がない場合は `UpdateService(desiredCount=0)` だけを呼ぶ。起動処理は target がある場合だけ min / max を登録し、その後に desired count を設定する既存の順序を維持する。

adapter test に、Aurora cluster と member の二重 tag、RDS Custom、非 Aurora cluster、および以下の ECS 条件を追加する。

- `REPLICA` を処理し、`DAEMON` では変更 API を呼ばない。
- 不正な ECS 設定タグでは、Start と Stop のどちらも変更 API を呼ばない。
- scalable target がある Stop は `RegisterScalableTarget(0, 0)` だけを呼ぶ。
- scalable target がない Stop は `UpdateService(desiredCount=0)` だけを呼ぶ。
- Start では設定タグの既定値、境界値、および `scaling-min <= desired-count <= scaling-max` を検証する。
- 旧 Stop の rollback test と rollback 実装を削除する。

### 6. reconcile

`internal/app/reconcile` からリース、Status の取得と更新、pending operation、error / recovered notification、グループ別 discovery、および claims を削除する。

処理を、強い整合性の設定 Query、望ましい状態の解決、固定 tag の全体探索、resource 処理、および summary の順に置き換える。設定と探索結果の全 page をメモリへ読み込むまで AWS リソースを操作しない。全体 discovery が失敗した場合は AWS リソースを 1 件も操作しない。

多重実行は排除しない。設定 snapshot が異なるサイクルによる相反操作と、次回サイクルでの収束を test する。

reconcile test に、次の条件を追加する。

- 重複 ARN へ 1 サイクル中に Start と Stop の両方を送らない。
- discovery error ではサイクル全体を中止する。
- resource error があっても他の resource を処理する。
- 設定変更前のサイクル後に、次のサイクルが最新の running / stopped へ収束させる。
- `disabled` と remove が動作中サイクルを取り消さない。
- DynamoDB へ書き込まない。
- 最終 override の書き込み完了後に開始した Query が変更後の設定を読み取る。

reconciler Lambda は呼び出し payload を解釈せず、すべての呼び出しで full reconcile を行う。自動呼び出しは 5 分間隔の schedule だけとし、RDS event 用の分岐、fixture、およびデプロイ設定を削除する。

リソース数による固定上限、分割実行、および checkpoint は追加しない。想定する最大数のタグ付きリソースを使用し、設定された Lambda timeout 内に full reconcile が完了することをデプロイ前に確認する。

### 7. CLI と Web コンソール

CLI command、help、JSON 型、および integration test を目標仕様へ置き換える。list の部分成功、終了コード 2、および条件付き書き込みの競合を test する。

Web コンソールから diagnostics、selector、mode、および timezone form を削除する。schedule と override の form は groups application を呼び出す。条件付き書き込みの競合は HTTP 409 とする。

reconciler と Web コンソールの `DEFAULT_TIMEZONE` は未設定時に UTC とし、起動時に検証する。image test で両 image の未設定時動作と不正値による起動失敗を確認する。

### 8. 通知とメトリクス

SNS adapter と呼び出し側をアクション通知だけに縮小する。error と recovered の notification 型と test を削除する。

`METRICS_ENABLED` の既定値は false のまま維持する。true の場合だけ既存のカスタムメトリクスを出力する。

CloudWatch log group、alarm、dashboard、および SNS topic をプロビジョニングするコード、template、script は追加しない。

### 9. IAM

reconciler の DynamoDB IAM は `CONFIG` partition の Query だけに限定する。CLI と Web コンソールは Query、GetItem、PutItem、UpdateItem、および DeleteItem だけを持つ。

IAM test では、reconciler に UpdateItem がなく、全 component に Status、Scan、BatchGetItem、およびリース用の権限がないことを確認する。

### 10. 開発環境とドキュメント

dev bootstrap、DynamoDB emulator、compose、system test、および image test の seed data を単一グループアイテム形式へ置き換える。所属タグは `cheapskate:group` だけにする。

日本語と英語の architecture、usage、setup、troubleshooting、および README を目標仕様へ更新する。現行ドキュメントに削除した仕様の説明を残さない。

## テスト範囲

実装段階を横断して確認する動作を、以下にまとめる。

| 分類 | 確認内容 |
| --- | --- |
| model | 属性間の不変条件、5 field cron、cron の到達可能性、override の時刻範囲 |
| state | strict decode、操作別の条件、delete race、属性単位の同時更新 |
| discovery | 全 page、設定 map への関連付け、ARN ごとの 1 回処理 |
| adapters | RDS / Aurora の対象判定、ECS REPLICA、ECS 設定 tag、scalable target 別の停止 API、EC2 state |
| reconcile | fail-closed、at-least-once、多重実行、後続サイクルでの収束 |
| CLI | command、JSON、終了コード、条件付き書き込みの競合 |
| Web | form、UTC 既定値、HTTP 409、security header、same-origin |
| metrics | 有効時の EMF と無効時の非出力 |
| image | reconciler と Web コンソールの実 image 動作 |

system test の一連動作は次のとおりである。

1. 空テーブルへ schedule group を作成する。
2. group tag が一致する resource を検出する。
3. schedule に従って停止する。
4. 期限付き `override running` によって起動する。
5. override 失効後に schedule へ戻る。
6. `override disabled` で新しいサイクルの Describe、Start、Stop を停止する。
7. concurrent remove と stale update が item を再作成しないことを確認する。
8. schedule と override の同時変更が互いの属性を失わないことを確認する。
9. group tag の変更中に同じ ARN へ Start と Stop を送らないことを確認する。
10. 不正な ECS 設定タグと `DAEMON` service に変更 API を送らないことを確認する。

## 検証コマンド

実装後に実行する検証を、以下に示す。

```console
make unit
make integration
make lint
make image
make image-test
```

削除漏れは対象拡張子を限定せず、リポジトリ内の全 text file から検索する。検索例を、以下に示す。

```console
rg -n 'pending_operation|transitioning_since|last_action|last_error|STATUS_RETENTION_DAYS' -g '!plan__to_slim.md' -g '!spec__slim.md' .
rg -n 'set-selector|selector-overlap|ModePinned|ModeSchedule|ModeDisabled|orphan-status|stuck-transitioning' -g '!plan__to_slim.md' -g '!spec__slim.md' .
rg -n 'doctor|diagnostics|AcquireLease|ReleaseLease|LOCK.*RECONCILE' -g '!plan__to_slim.md' -g '!spec__slim.md' .
rg -n 'Revision|dynamodbav:"revision"|RDS event' -g '!plan__to_slim.md' -g '!spec__slim.md' .
rg -n 'rollbackFailedStop|TestEcsStopRollsBackScaling|TestEcsStopReportsClampedTarget' -g '!plan__to_slim.md' -g '!spec__slim.md' .
```

検索結果が生成 mock、gohtml、JSON、YAML、Dockerfile、Makefile、および日本語・英語ドキュメントに残っていないことを確認する。

## 完了条件

本計画の完了条件は次のとおりである。

- [cheapskate slim 目標仕様](./spec__slim.md)の外部動作と不変条件を満たす。
- 設定の stale update と remove の競合で item を再作成しない。
- 同じ ARN へ 1 サイクル中に相反する AWS 操作を送らない。
- Aurora member、RDS Custom、および非 Aurora cluster へ不正な RDS API を送らない。
- ECS の scalable target がある場合は `RegisterScalableTarget(0, 0)` だけで停止する。
- 不正な ECS 設定タグと `DAEMON` service へ変更 API を送らない。
- 到達不能な cron を DynamoDB へ保存せず、読み取り時にも有効な設定として扱わない。
- 古い設定 snapshot を持つ invocation の終了後に、後続 reconcile が最新の running または stopped へ収束させる。
- 同じグループの schedule と override を同時に変更しても、一方の属性を失わない。
- reconciler の DynamoDB 権限が Query だけである。
- Status、doctor、リース、任意の selector、および旧 mode がコードと現行ドキュメントに存在しない。
- カスタムメトリクスが既定で無効であり、cheapskate が CloudWatch リソースをプロビジョニングしない。
- unit、integration、lint、image、および image-test が成功する。
