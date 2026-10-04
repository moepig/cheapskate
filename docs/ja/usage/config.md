# 実行時設定

各実行ファイルで使用する環境変数を次の表に示す。

| 変数 | 必須 | 既定値 | 用途 |
| --- | --- | --- | --- |
| `STATE_TABLE_NAME` | reconciler と Web コンソール | なし | DynamoDB table 名 |
| `DEFAULT_TIMEZONE` | いいえ | `UTC` | 全 schedule と Web の日時に適用する IANA time zone |
| `NOTIFICATION_TOPIC_ARN` | いいえ | 空 | 成功したアクションの通知先 |
| `METRICS_ENABLED` | いいえ | `false` | `true` の場合にカスタムメトリクスを出力する |
| `METRICS_NAMESPACE` | いいえ | `cheapskate` | カスタムメトリクスの namespace |
| `PORT` | いいえ | image は `8000`、バイナリ単体は未設定 | Web コンソールの HTTP listen port。指定時は `-addr` より優先し、`127.0.0.1` で待ち受ける |
| `AWS_LWA_PORT` | いいえ | image は `8000` | Lambda Web Adapter の接続先 port。`PORT` と一致させる |
| `BASE_PATH` | いいえ | 空 | URL path prefix |
| `ALLOWED_HOSTS` | いいえ | 待ち受けポート付きのループバックホスト | Web コンソールで許可する Host 値のカンマ区切りリスト |

reconciler と Web コンソールは、起動時に Go の time zone database で `DEFAULT_TIMEZONE` を検証する。不正な値では起動に失敗する。CLI も同じ変数を読み取り、未設定時は UTC を使用する。

NOTIFICATION_TOPIC_ARN、METRICS_ENABLED、および METRICS_NAMESPACE は reconciler だけが使用する。PORT、BASE_PATH、および ALLOWED_HOSTS は Web コンソールが使用する。

CLI の table 名は `-table` で指定できる。省略した場合は `CHEAPSKATE_TABLE`、次に `STATE_TABLE_NAME` を参照する。

`METRICS_ENABLED` を解釈できない場合は reconciler の起動に失敗する。空の `METRICS_NAMESPACE` には `cheapskate` を使用する。

Web コンソールをバイナリ単体で起動し、PORT を指定しない場合、待ち受けアドレスは -addr で指定する。既定値は 127.0.0.1:8080 である。BASE_PATH はリンク、form の送信先、およびリダイレクトへ付ける外部公開パスの接頭辞であり、受信ルートには付けない。接頭辞付きの URL を公開する場合、ingress からは接頭辞を除いたパスを転送すること。

ALLOWED_HOSTS は既定の許可リストを置き換える。未設定または空の場合は、localhost、127.0.0.1、および [::1] に待ち受けポートを付けた Host を許可する。全要求で、ポートを含む Host の値がリスト内の値と大文字と小文字を区別せず完全一致することを確認する。ワイルドカードとサブドメインの一致判定には対応しない。未許可の Host には、状態の読み取りや変更を行う前に HTTP 403 を返す。空白とカンマだけの値では起動に失敗する。

API Gateway や他の ingress を使用する場合は、Web コンソールへ転送される外部公開ホストの Host 値を ALLOWED_HOSTS に指定すること。複数ドメインの指定例は console.example,abc123.execute-api.ap-northeast-1.amazonaws.com である。ポート 8080 でのローカルアクセスの指定例は localhost:8080,127.0.0.1:8080 である。scheme と URL path は付けずに指定すること。
