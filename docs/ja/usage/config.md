# 実行時設定

本ドキュメントはreconciler、CLI、および Web コンソールの接続先、タイムゾーン、監視、HTTP 公開に関する設定を示す。

## 共通設定

AWS の認証情報、リージョン、および接続先は AWS SDK の設定を使用する。アプリケーション固有の共通環境変数を、以下に示す。

| 変数 | 必須 | 既定値 | 用途 |
| --- | --- | --- | --- |
| STATE_TABLE_NAME | reconciler と Web コンソール | なし | DynamoDB テーブル名 |
| DEFAULT_TIMEZONE | いいえ | UTC | スケジュールと Web の日時に適用する IANA タイムゾーン |

DEFAULT_TIMEZONE が不正な場合、reconciler と Web コンソールは起動に失敗する。CLI、reconciler、および Web コンソールでは同じ値を使用すること。

## reconciler

ログ、通知、およびメトリクスの変数を、以下に示す。

| 変数 | 既定値 | 用途 |
| --- | --- | --- |
| LOG_LEVEL | info | 最低ログレベル。debug、info、warn、error を指定可能 |
| NOTIFICATION_TOPIC_ARN | 空 | 成功したアクションの SNS 通知先 |
| METRICS_ENABLED | false | true の場合にカスタムメトリクスを出力 |
| METRICS_NAMESPACE | cheapskate | CloudWatch カスタムメトリクスの namespace |

LOG_LEVEL は大文字と小文字を区別せず、空の場合は info を使う。解釈できない値では起動に失敗する。

METRICS_ENABLED を真偽値として解釈できない場合は起動に失敗する。METRICS_NAMESPACE が空の場合は cheapskate を使う。出力内容は[ログ・メトリクス・通知](monitoring.md)を参照。

## CLI

CLI 固有の環境変数を、以下に示す。

| 変数 | 用途 |
| --- | --- |
| CHEAPSKATE_CONFIG | ローカルの CLI 設定ファイルのパス |
| CHEAPSKATE_TABLE | DynamoDB テーブル名 |

STATE_TABLE_NAME と DEFAULT_TIMEZONE も CLI で使用できる。context、オプション、環境変数の優先順位は、[実行形式と全体オプション](cli.md#実行形式と全体オプション)を参照。設定ファイルの保存先と形式は、[context](cli.md#context)を参照。

## Web コンソール

HTTP の待ち受けと外部公開に関する変数を、以下に示す。

| 変数 | 既定値 | 用途 |
| --- | --- | --- |
| PORT | イメージは 8000、バイナリ単体は未設定 | 指定すると -addr より優先し、127.0.0.1 で待ち受ける |
| AWS_LWA_PORT | イメージは 8000 | Lambda Web Adapter の接続先。PORT と一致させる |
| BASE_PATH | 空 | 外部公開 URL のパス接頭辞 |
| ALLOWED_HOSTS | 待ち受けポート付きのループバックホスト | 許可する Host 値のカンマ区切りリスト |

### 待ち受けアドレスとパス

バイナリ単体で PORT を指定しない場合は、-addr で待ち受けアドレスを指定する。既定値は 127.0.0.1:8080 である。

BASE_PATH はリンク、フォーム送信先、およびリダイレクトに付ける接頭辞であり、受信ルートには付けない。接頭辞付きの URL を公開する ingress は、接頭辞を除いたパスを転送すること。

### 公開ホスト

ALLOWED_HOSTS は既定の許可リストを置き換える。未設定または空の場合は、localhost、127.0.0.1、および [::1] に待ち受けポートを付けた Host を許可する。空白とカンマだけの指定では起動に失敗する。

要求の Host はポートを含め、大文字と小文字を区別せず完全一致で確認する。ワイルドカードとサブドメインの一致判定には対応しない。未許可の Host には状態の読み取りや変更の前に HTTP 403 を返す。

API Gateway などを使用する場合は、実際に転送される公開ホストを指定する。scheme と URL path は付けない。設定例を、以下に示す。

```text
ALLOWED_HOSTS=console.example,abc123.execute-api.ap-northeast-1.amazonaws.com
```

ポート 8080 でローカルアクセスする場合の例を、以下に示す。

```text
ALLOWED_HOSTS=localhost:8080,127.0.0.1:8080
```
