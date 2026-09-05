# 実行時設定

各実行ファイルで使用する環境変数を次の表に示す。

| 変数 | 必須 | 既定値 | 用途 |
| --- | --- | --- | --- |
| `STATE_TABLE_NAME` | reconciler と Web コンソール | なし | DynamoDB table 名 |
| `DEFAULT_TIMEZONE` | いいえ | `UTC` | 全 schedule と Web の日時に適用する IANA time zone |
| `NOTIFICATION_TOPIC_ARN` | いいえ | 空 | 成功したアクションの通知先 |
| `METRICS_ENABLED` | いいえ | `false` | `true` の場合にカスタムメトリクスを出力する |
| `METRICS_NAMESPACE` | いいえ | `cheapskate` | カスタムメトリクスの namespace |
| `PORT` | Web console image だけ | `8000` | Lambda Web Adapter と接続する HTTP listen port |
| `BASE_PATH` | Web コンソールだけ | 空 | URL path prefix |

reconciler と Web コンソールは、起動時に Go の time zone database で `DEFAULT_TIMEZONE` を検証する。不正な値では起動に失敗する。CLI も同じ変数を読み取り、未設定時は UTC を使用する。

CLI の table 名は `-table` で指定できる。省略した場合は `CHEAPSKATE_TABLE`、次に `STATE_TABLE_NAME` を参照する。

`METRICS_ENABLED` を解釈できない場合は reconciler の起動に失敗する。空の `METRICS_NAMESPACE` には `cheapskate` を使用する。
