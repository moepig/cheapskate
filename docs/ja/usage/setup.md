# 自分の AWS アカウントへのホスティング

IaC テンプレートは配布していません。このページが、自前でリソースを作成するための完全な契約(コントラクト)です。cheapskate が依存する設定値・IAM ポリシー・ペイロードはすべてここに記載しています。命名やツール選択(Terraform、CDK、CloudFormation、コンソール、CLI)は自由です。実行例は任意の IaC に 1:1 で読み替えられる AWS CLI で示し、プレースホルダにはアカウント `123456789012`、リージョン `ap-northeast-1` を使います。

作成するもの: DynamoDB テーブル、reconciler Lambda(コンテナイメージ)とその実行ロール、定期実行トリガー、RDS イベント用 EventBridge ルール。オプションで SNS トピック、ロググループ、Web コンソール。

## 1. コンテナイメージのビルドと push

イメージは公開レジストリから pull されることはありません。このリポジトリからビルドし、自分のアカウントの ECR リポジトリに push します:

```console
aws ecr create-repository --repository-name cheapskate   # 初回のみ
make push ECR_REPO=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate TAG=v0.1.0
```

デフォルトのプラットフォームは `linux/arm64`(どのホストでもエミュレーションなしでクロスコンパイル)。x86 が必要なら `make push PLATFORM=linux/amd64 ...` と Lambda アーキテクチャ `x86_64` を使ってください。関数からは push した URI(できれば digest 指定)を参照します。詳細は [../development/build.md](../development/build.md)。

## 2. DynamoDB state テーブル

- パーティションキー: `pk`(String)。ソートキー・GSI なし。
- 属性 `expires_at` で TTL を有効化(`override#` アイテムが使用)。
- 課金モードは任意。トラフィックは極小なのでオンデマンド推奨。

```console
aws dynamodb create-table --table-name cheapskate-state \
  --attribute-definitions AttributeName=pk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST
aws dynamodb update-time-to-live --table-name cheapskate-state \
  --time-to-live-specification "Enabled=true,AttributeName=expires_at"
```

## 3. SNS トピック(オプション)

通知はアクションが実行されたか失敗した場合にのみ送信されます。任意のトピックで構いません(関数は `sns:Publish` しか呼びません)。トピック(と環境変数 `NOTIFICATION_TOPIC_ARN`)を省略すると通知は無効になります。

```console
aws sns create-topic --name cheapskate-notifications
aws sns subscribe --topic-arn arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications \
  --protocol email --notification-endpoint ops@example.com
```

## 4. Lambda 実行ロール

信頼ポリシーは `lambda.amazonaws.com`。以下のポリシーをアタッチします(これで全部です — これ以外の API は呼びません):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "Logs",
      "Effect": "Allow",
      "Action": [
        "logs:CreateLogGroup",
        "logs:CreateLogStream",
        "logs:PutLogEvents"
      ],
      "Resource": "arn:aws:logs:ap-northeast-1:123456789012:log-group:/aws/lambda/*"
    },
    {
      "Sid": "State",
      "Effect": "Allow",
      "Action": [
        "dynamodb:Scan",
        "dynamodb:GetItem",
        "dynamodb:PutItem",
        "dynamodb:UpdateItem"
      ],
      "Resource": "arn:aws:dynamodb:ap-northeast-1:123456789012:table/cheapskate-state"
    },
    {
      "Sid": "RdsRead",
      "Effect": "Allow",
      "Action": ["rds:DescribeDBInstances", "rds:DescribeDBClusters"],
      "Resource": "*"
    },
    {
      "Sid": "EcsRead",
      "Effect": "Allow",
      "Action": ["ecs:DescribeServices"],
      "Resource": "*"
    },
    {
      "Sid": "Autoscaling",
      "Effect": "Allow",
      "Action": [
        "application-autoscaling:DescribeScalableTargets",
        "application-autoscaling:RegisterScalableTarget"
      ],
      "Resource": "*"
    },
    {
      "Sid": "Write",
      "Effect": "Allow",
      "Action": [
        "rds:StopDBInstance",
        "rds:StartDBInstance",
        "rds:StopDBCluster",
        "rds:StartDBCluster",
        "ecs:UpdateService"
      ],
      "Resource": "*"
    },
    {
      "Sid": "Notify",
      "Effect": "Allow",
      "Action": ["sns:Publish"],
      "Resource": "arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications"
    }
  ]
}
```

- `Describe*` と Application Auto Scaling の API はリソースレベル制限に対応していないため、そこは `"Resource": "*"` のままにします。
- stop/start をオプトインしたリソースだけに制限するには、`Write` ステートメントに `"Condition": {"StringEquals": {"aws:ResourceTag/cheapskate:managed": "true"}}`(タグのキー/値は任意。値は配列にすると複数指定の OR になります)を追加し、管理対象の RDS/ECS リソースにそのタグを付けます。複数のタグのいずれかで許可したい場合は、タグごとに `Write` ステートメントを複製します(同一 `Condition` 内に複数キーを書くと AND になるため)。
- 管理しないリソースタイプがある場合は、対応する Action を削除します(例: ECS を使わないなら `ecs:UpdateService` と `EcsRead` / `Autoscaling` ステートメント)。
- トピックなしで運用する場合は `Notify` ステートメントを削除します。

```console
aws iam create-role --role-name cheapskate-reconciler \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name cheapskate-reconciler \
  --policy-name cheapskate --policy-document file://policy.json
```

## 5. Reconciler Lambda 関数

| 設定                  | 値                                                                |
| --------------------- | ----------------------------------------------------------------- |
| パッケージタイプ      | `Image`(§1 の URI)                                                |
| アーキテクチャ        | `arm64`(または `x86_64` — イメージのプラットフォームと一致させる) |
| メモリ / タイムアウト | 256 MB / 120 秒                                                   |
| 予約同時実行数        | 1(reconcile の多重実行を防止)                                     |

環境変数(これが完全な契約です — これ以外は読みません):

| 変数                     | 必須   | 意味                                                |
| ------------------------ | ------ | --------------------------------------------------- |
| `STATE_TABLE_NAME`       | はい   | DynamoDB テーブル名                                 |
| `NOTIFICATION_TOPIC_ARN` | いいえ | SNS トピック ARN。空/未設定で通知無効               |
| `DEFAULT_TIMEZONE`       | いいえ | cron 評価に使う IANA タイムゾーン(デフォルト `UTC`) |

```console
aws lambda create-function --function-name cheapskate-reconciler \
  --package-type Image \
  --code ImageUri=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate:v0.1.0 \
  --architectures arm64 --memory-size 256 --timeout 120 \
  --role arn:aws:iam::123456789012:role/cheapskate-reconciler \
  --environment "Variables={STATE_TABLE_NAME=cheapskate-state,NOTIFICATION_TOPIC_ARN=arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications,DEFAULT_TIMEZONE=Asia/Tokyo}"
aws lambda put-function-concurrency --function-name cheapskate-reconciler \
  --reserved-concurrent-executions 1
```

同一アカウントの ECR から pull する場合、リポジトリポリシーは不要です。関数を作成するプリンシパルにリポジトリへの `ecr:BatchGetImage` と `ecr:GetDownloadUrlForLayer` が必要です。

推奨: ロググループを自分で作成し、保持期間が Lambda デフォルトの「無期限」にならないようにします:

```console
aws logs create-log-group --log-group-name /aws/lambda/cheapskate-reconciler
aws logs put-retention-policy --log-group-name /aws/lambda/cheapskate-reconciler --retention-in-days 30
```

## 6. 定期 reconcile トリガー

ペイロード `{}` で N 分おき(5 分がよいデフォルト)に関数を呼び出します。EventBridge Scheduler を使う場合:

- 実行ロール: 信頼ポリシーは `scheduler.amazonaws.com`。できれば `"Condition": {"StringEquals": {"aws:SourceAccount": "123456789012"}}` を付けます(confused deputy 対策)。権限は関数 ARN(qualifier を使うなら `<arn>:*` も)への `lambda:InvokeFunction`。
- スケジュール: `rate(5 minutes)`、`FlexibleTimeWindow: OFF`、ターゲットは関数、`Input: {}`。

```console
aws scheduler create-schedule --name cheapskate-reconcile \
  --schedule-expression "rate(5 minutes)" \
  --flexible-time-window Mode=OFF \
  --target '{"Arn":"arn:aws:lambda:ap-northeast-1:123456789012:function:cheapskate-reconciler","RoleArn":"arn:aws:iam::123456789012:role/cheapskate-scheduler","Input":"{}"}'
```

従来の EventBridge ルールの `rate()` 式でも同等に動きます(その場合はロールの代わりに §7 と同様の Lambda リソースベースポリシーを使います)。

## 7. EventBridge ルール(RDS 自動起動のファストパス)

AWS が停止中のインスタンス/クラスターを強制起動したとき、次のサイクルを待たずに即座に反応します。イベントパターン:

```json
{
  "source": ["aws.rds"],
  "detail-type": ["RDS DB Instance Event", "RDS DB Cluster Event"],
  "detail": {
    "EventID": [
      "RDS-EVENT-0154",
      "RDS-EVENT-0153",
      "RDS-EVENT-0088",
      "RDS-EVENT-0151"
    ]
  }
}
```

ターゲットは関数で、イベントは無加工で渡します。呼び出し許可は Lambda のリソースベースポリシーで与えます(ルールのターゲットに IAM ロールは関与しません):

```console
aws events put-rule --name cheapskate-rds-events --event-pattern file://pattern.json
aws events put-targets --rule cheapskate-rds-events \
  --targets '[{"Id":"reconciler","Arn":"arn:aws:lambda:ap-northeast-1:123456789012:function:cheapskate-reconciler"}]'
aws lambda add-permission --function-name cheapskate-reconciler \
  --statement-id cheapskate-rds-events --action lambda:InvokeFunction \
  --principal events.amazonaws.com \
  --source-arn arn:aws:events:ap-northeast-1:123456789012:rule/cheapskate-rds-events
```

## 8. 呼び出しペイロード(リファレンス)

- **定期 / 手動のフル reconcile**: `"source": "aws.rds"` を含まない任意の JSON オブジェクト — 標準形は `{}`。すべての `config#` アイテムを reconcile します。
- **RDS イベント**: EventBridge イベントそのもの(`source: aws.rds`、`detail.SourceType: DB_INSTANCE|CLUSTER`、`detail.SourceIdentifier`)。そのリソースだけを reconcile し、未登録のリソースは無視します。

## 9. Web コンソール(オプション)

`cheapskate-cli` と同じ操作をブラウザから行うフロントエンドです。**アクセス制御は IP 許可リストのみで、ログインはありません。**許可 CIDR の内側にいる人は誰でも操作できます。この割り切りが合わない場合はこの節をスキップしてください。デプロイせずローカルでテーブルに対して動かすこともできます([../development/run_local.md](../development/run_local.md))。

構成要素:

- **同じイメージからのもう 1 つの Lambda**。エントリポイントを `ImageConfig.EntryPoint: ["/var/runtime/webconsole"]` で上書きします。128 MB / 29 秒で十分です。環境変数: `STATE_TABLE_NAME`、`DEFAULT_TIMEZONE`、および `BASE_PATH`(ブラウザから見えるパスプレフィックス = 下記 API のステージ名 `/<stage>`)。
- **実行ロール**: state テーブルへの `dynamodb:Scan/GetItem/PutItem/DeleteItem` と §4 と同じ `Logs` ステートメントのみ。RDS/ECS の権限は意図的に持たせません。
- **API Gateway REST API**(v1 — HTTP API にはリソースポリシーがないため)。ルートリソースと `{proxy+}` の両方に Lambda への `ANY` プロキシ統合を張り、`apigateway.amazonaws.com` への Lambda permission を付与します。アクセス制御はリソースポリシーだけです:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": "*",
      "Action": "execute-api:Invoke",
      "Resource": "execute-api:/*"
    },
    {
      "Effect": "Deny",
      "Principal": "*",
      "Action": "execute-api:Invoke",
      "Resource": "execute-api:/*",
      "Condition": {
        "NotIpAddress": {
          "aws:SourceIp": ["203.0.113.0/24", "198.51.100.7/32"]
        }
      }
    }
  ]
}
```

`BASE_PATH` と一致する名前のステージ(例: ステージ `console`、`BASE_PATH=/console`)にデプロイします。入口は `https://<api-id>.execute-api.<region>.amazonaws.com/console/` です。

## 10. デプロイの検証

1. 稼働中の開発用 RDS インスタンスに `mode: pinned`、`desired: stopped` の `config#` アイテムを登録する(`cheapskate-cli pin rds-instance#<id> stopped`、[operations.md](operations.md) 参照)→ 1 インターバル以内に `stopping` へ遷移し、`status#` アイテムに `last_action: stop` が記録されること。
2. コンソールから手動で起動する → 1 インターバル以内に再び停止されること(ドリフト補正)。
3. `mode: schedule` の ECS サービスを登録する → cron の境界で desiredCount が切り替わること(停止で 0、起動で `restore_count`)。
4. 通知を設定している場合、各アクションで SNS メッセージが 1 通発行され、収束済みサイクルでは発行されないこと。
