# 環境変数リファレンス

本ドキュメントは、reconciler、Web コンソール、`cheapskate-cli` が読む環境変数を規定する。

## Reconciler

読む環境変数を、以下に示す。これ以外の環境変数は読まない。

| 変数 | 必須 | 意味 |
| --- | --- | --- |
| `STATE_TABLE_NAME` | はい | DynamoDB テーブル名 |
| `NOTIFICATION_TOPIC_ARN` | いいえ | SNS トピック ARN。空または未設定で通知を無効にする |
| `DEFAULT_TIMEZONE` | いいえ | cron 評価に使う IANA タイムゾーン(既定 `UTC`) |
| `METRICS_ENABLED` | いいえ | CloudWatch メトリクスを発行するか(既定 `true`)。真偽値(`true`/`false`、`1`/`0`) |
| `METRICS_NAMESPACE` | いいえ | CloudWatch メトリクスの名前空間(既定 `cheapskate`) |

`METRICS_ENABLED` が解釈できない値(`fasle` など)である場合、既定値へ倒さず起動を失敗させる。`METRICS_NAMESPACE` は未設定でも空文字列でも既定の `cheapskate` となるため、メトリクスの無効化には `METRICS_ENABLED` を用いる。

メトリクスを無効にすると、失敗件数とアクション件数の推移が観測できなくなる。失敗の検知そのものは、Lambda 組み込みの `Errors` メトリクスと SNS 通知が引き続き担う。無効時は、コールドスタートごとに `metrics-disabled` のログを 1 行出力する。

## Web コンソール

読む環境変数を、以下に示す。

| 変数 | 必須 | 意味 |
| --- | --- | --- |
| `STATE_TABLE_NAME` | はい | DynamoDB テーブル名(`CHEAPSKATE_TABLE` でも可) |
| `DEFAULT_TIMEZONE` | いいえ | cron 表示に使う IANA タイムゾーン(既定はサーバーのローカル時刻) |
| `BASE_PATH` | いいえ | API Gateway のステージ名を含むベースパス(例 `/console`)。未設定ならルート直下 |
| `PORT` | いいえ | 待ち受けポート(`127.0.0.1` に固定)。未設定なら `-addr` フラグの値(既定 `127.0.0.1:8080`) |

> [!NOTE]
> `PORT` はコンテナイメージ側で設定済みであり、Lambda へのデプロイ時に指定する対象ではない。

## cheapskate-cli

読む環境変数を、以下に示す。

| 変数 | 必須 | 意味 |
| --- | --- | --- |
| `CHEAPSKATE_TABLE` | いいえ | DynamoDB テーブル名(`-table` フラグでも指定可) |
