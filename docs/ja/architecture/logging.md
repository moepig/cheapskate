# ログ

## ログの種別

コンポーネントごとのログの形式と用途を、以下に示す。

| コンポーネント  | 形式                        | 出力先 | 用途                                    |
| --------------- | --------------------------- | ------ | --------------------------------------- |
| reconciler      | `log/slog` の JSON ハンドラ | stderr | 実行ログ                                |
| Web コンソール  | `log/slog` の JSON ハンドラ | stderr | 起動ログ, リクエストログ, エラーログ    |
| `dev-bootstrap` | 標準 `log`                  | stderr | `make dev` 専用。イメージには含まれない |

## JSON 形式ログ

reconciler と Web コンソールは、どちらも `log/slog` の JSON ハンドラで 1 レコード 1 行の JSON を stderr に出力する。

### レコードの例

reconciler が 1 件のアクションを実行したときに出力するレコードを、以下に示す。

```json
{
  "time": "2026-07-15T03:00:01Z",
  "level": "INFO",
  "msg": "action",
  "group": "dev",
  "resource_id": "rds-instance#dev-db",
  "action": "stop",
  "desired": "stopped"
}
```

### 属性

必須欄の ✓ は、どのレコードにも必ず入ることを示す。✓ でない属性が入るかどうかはログ種別ごとに決まり、その対応は各コンポーネントのイベント一覧が持つ。

同じ名前の属性は、コンポーネントをまたいでも同じ意味で用いる。全属性を、以下に示す。

| 属性          | 必須 | 型     | 意味                                            | 形式・値域                                                     |
| ------------- | ---- | ------ | ----------------------------------------------- | -------------------------------------------------------------- |
| `time`        | ✓    | string | レコードを出した時刻                            | RFC3339(`log/slog` の既定)                                     |
| `level`       | ✓    | string | 重大度                                          | `INFO`, `WARN`, `ERROR`                                        |
| `msg`         | ✓    | string | ログの種別                                      | 各コンポーネントのイベント一覧の値                             |
| `group`       | -    | string | グループ名                                      | グループ名として妥当な文字列                                   |
| `resource_id` | -    | string | 対象リソースの ID                               | `<リソース種別>#<名前>`。グループ単位の失敗では `group#<名前>` |
| `desired`     | -    | string | スケジュールから決まった、あるべき状態          | `running`, `stopped`                                           |
| `detail`      | -    | string | 観測した状態の、リソース種別ごとの補足          | 自由文(例 `desiredCount=2`)                                    |
| `since`       | -    | string | 遷移中とみなし始めた時刻                        | RFC3339(UTC)                                                   |
| `source`      | -    | string | 起動の契機になった EventBridge イベントの発生元 | イベントの `source`(例 `aws.rds`)                              |
| `reason`      | -    | string | そう扱った理由                                  | 自由文                                                         |
| `action`      | -    | string | 実行した、あるいは要求された操作                | 各コンポーネントのアクション一覧の値                           |
| `result`      | -    | string | 操作の結果                                      | 自由文(画面に出すのと同じ文言)                                 |
| `reconciled`  | -    | number | 1 サイクルで見たリソース数                      | 0 以上の整数                                                   |
| `actions`     | -    | number | 1 サイクルで実行したアクション数                | 0 以上の整数                                                   |
| `errors`      | -    | number | 1 サイクルの失敗数                              | 0 以上の整数                                                   |
| `pruned`      | -    | number | prune で削除できたレコード数                    | 0 以上の整数。一部の削除に失敗したときだけ入る                 |
| `status`      | -    | number | 返した HTTP ステータス                          | HTTP ステータスコード                                          |
| `duration_ms` | -    | number | リクエストの受信から完了までの所要時間          | ミリ秒(整数)                                                   |
| `table`       | -    | string | 状態を置く DynamoDB テーブル                    | テーブル名                                                     |
| `base_path`   | -    | string | ブラウザから見えるパスのプレフィックス          | `BASE_PATH` の値。未設定なら空文字列                           |
| `timezone`    | -    | string | 既定のタイムゾーン                              | IANA タイムゾーン名(例 `Asia/Tokyo`)                           |
| `addr`        | -    | string | 待ち受けアドレス                                | `host:port`。Lambda 上でもローカルでも入る                     |
| `method`      | -    | string | HTTP メソッド                                   | `GET`, `POST`                                                  |
| `path`        | -    | string | リクエストのパス                                | `BASE_PATH` を含まないパス                                     |
| `query`       | -    | string | クエリ文字列                                    | 生の文字列。無ければ空文字列                                   |
| `client`      | -    | string | リクエスト元の IP                               | IP アドレス(ポートは付かない)。Lambda 上ではイベントの `requestContext` の `sourceIp`(= リソースポリシーが許可判定に使った IP)であり、`X-Forwarded-For` は見ない |
| `error`       | -    | string | 失敗の内容                                      | `err.Error()` の文字列。ラップされた原因も含む                 |
| `_aws`        | -    | object | EMF のメタデータ([metrics.md](metrics.md))      | 同じレコードにメトリクス名ごとの number が並ぶ                 |

> [!NOTE]
> Lambda 関数内の初期化処理で失敗した場合に限り、上記の一覧にない任意のエラーメッセージが `msg` に入る。

## reconciler

### イベント一覧

reconciler が出力するイベントを、以下に示す。

| `msg`                        | level | 属性                                        | 意味                                                |
| ---------------------------- | ----- | ------------------------------------------- | --------------------------------------------------- |
| `event-received`             | INFO  | `source`, `reason`                          | EventBridge のイベント経由の起動ログ                |
| `orphaned-group-data`        | WARN  | `group`                                     | グループにおける DynamoDB 上のデータ不整合          |
| `action`                     | INFO  | `group`, `resource_id`, `action`, `desired` | リソースの start/stop を実行した                    |
| `skip-transitioning`         | INFO  | `resource_id`, `detail`, `since`            | 処理のスキップ (遷移中とみなし処理を見送った場合)   |
| `skip-not-found`             | INFO  | `resource_id`                               | 処理のスキップ (リソースが存在しない)               |
| `error`                      | ERROR | `group`, `resource_id`, `error`             | リソース単位・グループ単位の失敗                    |
| `summary`                    | INFO  | `reconciled`, `actions`, `errors`           | 1 サイクルの実行結果サマリ                          |
| `metrics`                    | INFO  | `_aws` + メトリクス名                       | EMF メトリクス([metrics.md](metrics.md))            |
| `metrics-disabled`           | INFO  | `reason`                                    | メトリクスの有効/無効を示す                         |
| `action-notify-failed`       | ERROR | `group`, `resource_id`, `error`             | アクション実行の通知の失敗                          |
| `recovery-notify-failed`     | ERROR | `group`, `resource_id`, `error`             | 復旧(`recovered`)通知の失敗                         |
| `error-notify-failed`        | ERROR | `group`, `resource_id`, `error`             | 失敗の通知の失敗                                    |
| `error-clear-failed`         | ERROR | `group`, `resource_id`, `error`             | エラーから回復した場合の `last_error` クリアの失敗  |
| `error-record-failed`        | ERROR | `group`, `resource_id`, `error`             | `last_error` への失敗の書き込みの失敗               |
| `status-read-failed`         | ERROR | `group`, `resource_id`, `error`             | 通知の重複排除に使う前回 `status#` の読み取りの失敗 |
| `transitioning-mark-failed`  | ERROR | `resource_id`, `error`                      | `transitioning_since` の書き込み失敗                |
| `transitioning-clear-failed` | ERROR | `resource_id`, `error`                      | `transitioning_since` のクリア失敗                  |

### アクション一覧

`action` 属性が取る値を、以下に示す。

| `action` | 意味                           |
| -------- | ------------------------------ |
| `start`  | 停止しているリソースを起動した |
| `stop`   | 稼働しているリソースを停止した |

### ログのみに記録するエラー

エラーの記録先は、ログ、`status#` の `last_error`、および SNS 通知の 3 つである。ただし次のエラーは、記録経路そのものの失敗を表すため、ログにのみ記録する。

| イベント                                    | ログのみとする理由                                        |
| ------------------------------------------- | --------------------------------------------------------- |
| `*-notify-failed`                           | 通知に失敗しているため                                    |
| `status-read-failed`, `error-record-failed` | DynamoDB にアクセスできず、記録できないことを記録できない |
| `transitioning-*-failed`                    | 監査のための情報であり、reconciler の失敗扱いとしない     |

## Web コンソール

### イベント一覧

Web コンソールが出力するイベントを、以下に示す。

| `msg`              | level | 属性                                                           | 意味                                            |
| ------------------ | ----- | -------------------------------------------------------------- | ----------------------------------------------- |
| `startup`          | INFO  | `table`, `base_path`, `timezone`, `addr`                       | 起動ログ(コールドスタートごとに 1 行)           |
| `request-start`    | INFO  | `method`, `path`, `query`, `client`                            | リクエストの受信                                |
| `request-end`      | INFO  | `request-start` の属性 + `status`, `duration_ms`               | リクエストの完了                                |
| `request-failed`   | ERROR | `request-start` の属性 + `status`, `error`                     | 4xx/5xx を返した(画面にも同じ文言を表示する)    |
| `operation`        | INFO  | `action`, `group`, `client`, `result`                          | 設定を書き換える操作の成功                      |
| `operation-failed` | ERROR | `action`, `group`, `client`, `error`, `pruned`                 | 同上の失敗(画面へは `?err=` でリダイレクトする) |

> [!NOTE]
> `action` が `doctor-prune` の場合は `group` が含まれない。`pruned` が付くのは、prune 自体は実行されたが個々のレコードの削除に一部失敗した場合のみである。

起動できなかった場合は、上記のイベント外で ERROR を 1 行出力し、終了コード 1 で終了する。

### アクション一覧

`action` 属性が取る値を、以下に示す。

| `action`         | 意味                                           |
| ---------------- | ---------------------------------------------- |
| `set-selector`   | グループのセレクタを保存した(無ければ作る)     |
| `schedule`       | グループのスケジュールを保存した               |
| `pin`            | スケジュールを無視して状態を固定した           |
| `unpin`          | 固定を解除した                                 |
| `override`       | 期限付きでスケジュールより優先する状態を入れた |
| `clear-override` | 期限付きの優先を消した                         |
| `disable`        | グループを収束の対象から外した                 |
| `remove-group`   | グループを削除した                             |
| `doctor-prune`   | 孤立レコードを削除した                         |
