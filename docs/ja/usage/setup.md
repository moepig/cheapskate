# AWS アカウントへのホスティング

IaC テンプレートは配布しない。本ドキュメントは、作成する AWS リソースの仕様をすべて記載する。使用するツール(Terraform、CDK、CloudFormation、マネジメントコンソール、CLI)と命名は任意である。実行例は AWS CLI で示し、プレースホルダはアカウント `123456789012`、リージョン `ap-northeast-1` とする。

作成するリソースは次のとおりである。

| 節 | リソース | 必須 |
|---|---|---|
| §1 | コンテナイメージ(ECR) | 必須 |
| §2 | DynamoDB state テーブル | 必須 |
| §3 | SNS トピックと監視 | オプション |
| §4 | Lambda 実行ロール | 必須 |
| §5 | Reconciler Lambda 関数 | 必須 |
| §6 | 定期 reconcile トリガー | 必須 |
| §7 | EventBridge ルール(RDS 自動起動) | 必須 |
| §9 | Web コンソール | オプション |

## 1. コンテナイメージの ECR への配置

reconciler とオプションの Web コンソールは別々のイメージであり、バイナリを 1 つずつ含む。Lambda が pull できるのは ECR のみであるから、以下のいずれの方法でも、最終的にイメージを ECR へ配置する。

```console
aws ecr create-repository --repository-name cheapskate-reconciler   # 初回のみ
aws ecr create-repository --repository-name cheapskate-webconsole   # 初回のみ(§9 を使う場合)
```

### リリース済みイメージの複製

リリースごとに、両方のイメージが `linux/amd64` と `linux/arm64` 向けに GHCR へ公開される。`--platform` で関数が動作する 1 アーキテクチャを選ぶため、ECR に置かれるのは単一アーキテクチャのイメージとなる。

```console
aws ecr get-login-password | docker login --username AWS --password-stdin 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com
docker pull --platform linux/arm64 ghcr.io/moepig/cheapskate-reconciler:v0.1.0
docker tag ghcr.io/moepig/cheapskate-reconciler:v0.1.0 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-reconciler:v0.1.0
docker push 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-reconciler:v0.1.0
```

### ソースからのビルド

`make push` が、両イメージのビルドから ECR への push までを行う。

```console
make push \
  ECR_REPO_RECONCILER=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-reconciler \
  ECR_REPO_WEBCONSOLE=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-webconsole \
  TAG=v0.1.0
```

Web コンソールを使わない場合は `make push-reconciler ECR_REPO_RECONCILER=... TAG=v0.1.0` で足りる。Lambda 関数からは push した URI を参照する。参照は digest 指定を推奨する。

既定のプラットフォームは `linux/arm64` である。x86 を使う場合は `make push PLATFORM=linux/amd64 ...` とし、Lambda アーキテクチャを `x86_64` とする。

## 2. DynamoDB state テーブル

テーブルに要求する定義を、以下に示す。

| 項目 | 値 |
|---|---|
| パーティションキー | `pk`(String) |
| ソートキー / GSI | なし |
| TTL | 属性 `expires_at` で有効化する |
| 課金モード | 任意(オンデマンド推奨) |

```console
aws dynamodb create-table --table-name cheapskate-state \
  --attribute-definitions AttributeName=pk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST
aws dynamodb update-time-to-live --table-name cheapskate-state \
  --time-to-live-specification "Enabled=true,AttributeName=expires_at"
```

## 3. SNS トピックと監視(オプション)

### SNS トピック

アクションの実行時と失敗時に通知を送る先である。関数が呼ぶ API は `sns:Publish` のみである。トピックと環境変数 `NOTIFICATION_TOPIC_ARN` を省略した場合、通知は無効となる。

```console
aws sns create-topic --name cheapskate-notifications
aws sns subscribe --topic-arn arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications \
  --protocol email --notification-endpoint ops@example.com
```

通知は 3 種類である。件名は `[cheapskate] <種別>: <グループ名>/<リソース ID>`、本文は JSON オブジェクト 1 つである。種別と送信の契機を、以下に示す。

| 種別 | 送信される契機 |
| --- | --- |
| `start` / `stop` | リソースの起動・停止を実行したとき |
| `error` | 失敗を記録したとき。同一エラーの継続中は再送されない |
| `recovered` | 記録済みのエラーが解消したとき |

### メトリクス

reconciler は毎サイクル、4 つのメトリクスを出力する。名前空間は `METRICS_NAMESPACE`(既定 `cheapskate`)、次元なし、単位は Count である。`PutMetricData` を呼ばず CloudWatch Logs 経由で生成されるため、追加の IAM 権限を要しない。出力するメトリクスを、以下に示す。

| メトリクス | 意味 |
| --- | --- |
| `ReconciledResources` | そのサイクルで処理したリソース件数 |
| `ReconcileActions` | 実行した start/stop の件数 |
| `ReconcileErrors` | リソース単位・グループ単位の失敗件数 |
| `ReconcileAborted` | サイクル自体が立ち上がらなかったとき 1、通常は 0 |

この 4 本はカスタムメトリクスとして課金される(合計で月 1 ドル強)。不要であれば `METRICS_ENABLED=false` で発行を止められる。

### 失敗検知のアラーム

通知とは別に、失敗の検知には Lambda の `Errors` メトリクスへのアラームを設定する。設定するアラームの具体例と、それぞれが何を捉えるかは、[troubleshooting.md](troubleshooting.md) の障害の検知を参照。

> [!WARNING]
> SNS トピックと `Errors` アラームのいずれも用意しない場合、全リソースが失敗し続けても検知経路が存在しない。少なくとも一方を設定すること。

## 4. Lambda 実行ロール

信頼ポリシーは `lambda.amazonaws.com` とする。アタッチするポリシーは次のとおりであり、これ以外の API は呼ばない。

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
      "Sid": "StateRead",
      "Effect": "Allow",
      "Action": ["dynamodb:Scan", "dynamodb:GetItem"],
      "Resource": "arn:aws:dynamodb:ap-northeast-1:123456789012:table/cheapskate-state"
    },
    {
      "Sid": "StateWriteStatusOnly",
      "Effect": "Allow",
      "Action": ["dynamodb:UpdateItem"],
      "Resource": "arn:aws:dynamodb:ap-northeast-1:123456789012:table/cheapskate-state",
      "Condition": {
        "ForAllValues:StringLike": {"dynamodb:LeadingKeys": ["status#*"]}
      }
    },
    {
      "Sid": "TagDiscovery",
      "Effect": "Allow",
      "Action": ["tag:GetResources"],
      "Resource": "*"
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
      "Sid": "Ec2Read",
      "Effect": "Allow",
      "Action": ["ec2:DescribeInstances"],
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
        "ecs:UpdateService",
        "ec2:StartInstances",
        "ec2:StopInstances"
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

このポリシーをロールへ適用する手順を、以下に示す。

```console
aws iam create-role --role-name cheapskate-reconciler \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name cheapskate-reconciler \
  --policy-name cheapskate --policy-document file://policy.json
```

削除してよい記述は、管理しないリソースタイプの Action と、SNS トピックを使わない場合の `Notify` である。

### DynamoDB の権限を分けている理由

reconciler が書くのは `status#` アイテムだけである。したがって `dynamodb:PutItem` は一切付与せず、`UpdateItem` も `dynamodb:LeadingKeys` 条件で `status#*` に閉じる。`Scan` は `LeadingKeys` 条件と併用できないため、読み取り専用の別ステートメントに分けてある。

1 つにまとめる場合は条件なしの `["dynamodb:Scan", "dynamodb:GetItem", "dynamodb:UpdateItem"]` でよいが、関数からスケジュールを書き換えられないという保証は失われる。

### Resource をワイルドカードにしている理由

`tag:GetResources`、`Describe*`、Application Auto Scaling の API は、いずれもリソースレベル制限に非対応である。

### 停止/起動の対象を絞る方法

`Write` ステートメントに次の条件を追加し、管理対象のリソースに同じタグを付与する。タグのキーと値は任意である。

```json
"Condition": {"StringEquals": {"aws:ResourceTag/cheapskate:managed": "true"}}
```

値を配列にすると OR 条件となる。複数タグの OR はステートメントを複製して表現する。同一 `Condition` 内の複数キーは AND となる。

## 5. Reconciler Lambda 関数

関数に要求する設定を、以下に示す。

| 設定 | 値 |
| --- | --- |
| パッケージタイプ | `Image`(§1 の URI) |
| アーキテクチャ | `arm64`(または `x86_64` — イメージのプラットフォームと一致させる) |
| メモリ / タイムアウト | 256 MB / 120 秒 |
| 予約同時実行数 | 1(reconcile の多重実行を防止する) |

関数に設定する環境変数の一覧と既定値は [config.md](config.md) にある。

```console
aws lambda create-function --function-name cheapskate-reconciler \
  --package-type Image \
  --code ImageUri=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-reconciler:v0.1.0 \
  --architectures arm64 --memory-size 256 --timeout 120 \
  --role arn:aws:iam::123456789012:role/cheapskate-reconciler \
  --environment "Variables={STATE_TABLE_NAME=cheapskate-state,NOTIFICATION_TOPIC_ARN=arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications,DEFAULT_TIMEZONE=Asia/Tokyo}"
aws lambda put-function-concurrency --function-name cheapskate-reconciler \
  --reserved-concurrent-executions 1
```

> [!NOTE]
> 同一アカウントの ECR から pull する場合、リポジトリポリシーを要しない。ただし、関数を作成するプリンシパルには `ecr:BatchGetImage` と `ecr:GetDownloadUrlForLayer` が必要である。

ロググループは事前に作成し、保持期間を設定することを推奨する。Lambda の既定は無期限である。

```console
aws logs create-log-group --log-group-name /aws/lambda/cheapskate-reconciler
aws logs put-retention-policy --log-group-name /aws/lambda/cheapskate-reconciler --retention-in-days 30
```

## 6. 定期 reconcile トリガー

ペイロード `{}` で N 分おきに関数を呼び出す(推奨は 5 分)。EventBridge Scheduler を使う場合の設定を、以下に示す。

| 項目 | 値 |
| --- | --- |
| 実行ロールの信頼ポリシー | `scheduler.amazonaws.com` |
| 実行ロールの権限 | 関数 ARN への `lambda:InvokeFunction`(qualifier 使用時は `<arn>:*` も) |
| スケジュール式 | `rate(5 minutes)` |
| `FlexibleTimeWindow` | `OFF` |
| ターゲット | 関数、`Input: {}` |

confused deputy 対策として、信頼ポリシーへの `"Condition": {"StringEquals": {"aws:SourceAccount": "123456789012"}}` の追加を推奨する。

```console
aws scheduler create-schedule --name cheapskate-reconcile \
  --schedule-expression "rate(5 minutes)" \
  --flexible-time-window Mode=OFF \
  --target '{"Arn":"arn:aws:lambda:ap-northeast-1:123456789012:function:cheapskate-reconciler","RoleArn":"arn:aws:iam::123456789012:role/cheapskate-scheduler","Input":"{}"}'
```

EventBridge ルールの `rate()` 式でも動作する。その場合は、ロールの代わりに §7 と同様の Lambda リソースベースポリシーを使う。

## 7. EventBridge ルール(RDS 自動起動)

停止中の RDS が AWS により自動起動されたとき、次の定期サイクルを待たずに reconcile をトリガーする。購読するのは起動の完了イベントだけである。自動起動の開始を知らせる `RDS-EVENT-0153` / `RDS-EVENT-0154` の時点ではリソースが `starting` であり停止 API を呼べないため、その時点での呼び出しは必ず空振りするためである。

```json
{
  "source": ["aws.rds"],
  "detail-type": ["RDS DB Instance Event", "RDS DB Cluster Event"],
  "detail": {
    "EventID": ["RDS-EVENT-0088", "RDS-EVENT-0151"]
  }
}
```

ターゲットは関数とし、イベントは無加工で渡す。呼び出し許可は Lambda のリソースベースポリシーで与える。ルールのターゲットに IAM ロールは使わない。

```console
aws events put-rule --name cheapskate-rds-events --event-pattern file://pattern.json
aws events put-targets --rule cheapskate-rds-events \
  --targets '[{"Id":"reconciler","Arn":"arn:aws:lambda:ap-northeast-1:123456789012:function:cheapskate-reconciler"}]'
aws lambda add-permission --function-name cheapskate-reconciler \
  --statement-id cheapskate-rds-events --action lambda:InvokeFunction \
  --principal events.amazonaws.com \
  --source-arn arn:aws:events:ap-northeast-1:123456789012:rule/cheapskate-rds-events
```

## 8. 呼び出しペイロード

任意の JSON オブジェクトがフル reconcile をトリガーする。定期実行の `{}` も RDS イベントも同様である。ペイロードの内容によって処理の範囲が変わることはない。

## 9. Web コンソール(オプション)

`cheapskate-cli` と同じ操作をブラウザから行うためのコンポーネントである。デプロイせずローカルで動かすこともできる。

> [!WARNING]
> アクセス制御は IP 許可リストのみであり、ログインは無い。許可 CIDR 内の全員が操作できる。この条件が受け入れられない場合は、本節を構築しないこと。

### Lambda 関数

§1 で push した `cheapskate-webconsole` イメージを使う別関数とする。専用イメージであるため `ImageConfig.EntryPoint` の上書きを要しない。メモリとタイムアウトは 128 MB / 29 秒とする。環境変数の一覧は [config.md](config.md) にあり、`BASE_PATH` には下記ステージ名 `/<stage>` を設定する。イベントを HTTP に変換する Lambda Web Adapter はイメージに同梱されており、レイヤーの追加も設定も要しない。

### 実行ロール

state テーブルへの `dynamodb:Scan/GetItem/PutItem/DeleteItem`、`tag:GetResources`、現在の状態を表示するための下記の読み取り専用 `Describe*`、および §4 と同じ `Logs` のみを付与する。RDS/ECS/EC2 の制御系権限は付与しない。`dynamodb:UpdateItem` も付与しない。これは `status#` レコードを書ける唯一の経路であるためである。

```json
{
  "Sid": "LiveStateRead",
  "Effect": "Allow",
  "Action": [
    "rds:DescribeDBInstances",
    "rds:DescribeDBClusters",
    "ecs:DescribeServices",
    "ec2:DescribeInstances"
  ],
  "Resource": "*"
}
```

コンソールから AWS へ一切問い合わせない場合、このステートメントは不要である。その場合、現在の状態の列が行ごとに access-denied を表示するだけであり、ページの他の部分は動作する。管理しないリソースタイプの Action も個別に削除してよい。

`tag:GetResources` が無い場合、グループページは検出エラーを表示する。コンソール自体は動作する。

### API Gateway REST API(v1)

ルートリソースと `{proxy+}` の両方に Lambda への `ANY` プロキシ統合を張り、`apigateway.amazonaws.com` への Lambda permission を付与する。アクセス制御は次のリソースポリシーで行う。

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

`BASE_PATH` と同名のステージ(例: ステージ `console`、`BASE_PATH=/console`)にデプロイする。URL は `https://<api-id>.execute-api.<region>.amazonaws.com/console/` となる。

## 10. 設定の投入

以上でリソースの作成は完了する。この時点では state テーブルが空であり、reconciler は毎サイクル何も操作しない。管理対象を決めるには、グループの設定レコードを追加し、対象の AWS リソースにセレクタのタグを付与する。詳細は、[operations.md](operations.md) の追加、および [resource_tag.md](resource_tag.md) のセレクタのタグを参照。
