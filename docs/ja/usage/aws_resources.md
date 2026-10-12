# AWS リソースと IAM

本ドキュメントは、配置に必要な AWS リソースと、reconciler・管理インターフェースに付与するアクセス権限を示す。

## 配置する AWS リソース

必要なリソースと用途を、以下に示す。

| リソース | 用途 |
| --- | --- |
| DynamoDB テーブル | グループ設定の保存 |
| reconciler Lambda と実行ロール | タグ付きリソースの定期制御 |
| EventBridge Scheduler または EventBridge rule | 5 分間隔の Lambda 呼び出しと呼び出し権限 |
| CloudWatch Logs のロググループ | 実行ログの保存 |
| ECR リポジトリ | 配置するコンテナイメージの保存 |
| Web コンソールと認証付き ingress | 任意のブラウザ操作 |
| SNS topic と subscription | 任意の成功アクション通知 |
| CloudWatch alarm と dashboard | 任意の監視 |

cheapskate は、これらのリソースを自動作成しない。配置手順は、[セットアップ](setup.md)を参照。

## reconciler の権限

管理するリソース種別に応じて、以下の権限を付与する。

| サービス | IAM action |
| --- | --- |
| DynamoDB | dynamodb:Query |
| Resource Groups Tagging API | tag:GetResources |
| RDS | rds:DescribeDBInstances、rds:StartDBInstance、rds:StopDBInstance、rds:DescribeDBClusters、rds:StartDBCluster、rds:StopDBCluster |
| ECS | ecs:DescribeServices、ecs:UpdateService |
| Application Auto Scaling | application-autoscaling:DescribeScalableTargets、application-autoscaling:RegisterScalableTarget |
| EC2 | ec2:DescribeInstances、ec2:StartInstances、ec2:StopInstances |
| SNS | 設定した topic への sns:Publish |
| CloudWatch Logs | Lambda のログ配信権限 |

DynamoDB には読み取りだけを許可する。CONFIG パーティションに限定する policy の例を、以下に示す。REGION、ACCOUNT、および TABLE は配置先に置き換える。

```json
{
  "Effect": "Allow",
  "Action": "dynamodb:Query",
  "Resource": "arn:aws:dynamodb:REGION:ACCOUNT:table/TABLE",
  "Condition": {
    "ForAllValues:StringEquals": {
      "dynamodb:LeadingKeys": ["CONFIG"]
    }
  }
}
```

## CLI と Web コンソールの権限

グループ設定の読み書きに必要な policy の例を、以下に示す。

```json
{
  "Effect": "Allow",
  "Action": [
    "dynamodb:Query",
    "dynamodb:GetItem",
    "dynamodb:PutItem",
    "dynamodb:UpdateItem",
    "dynamodb:DeleteItem"
  ],
  "Resource": "arn:aws:dynamodb:REGION:ACCOUNT:table/TABLE",
  "Condition": {
    "ForAllValues:StringEquals": {
      "dynamodb:LeadingKeys": ["CONFIG"]
    }
  }
}
```

show とグループ詳細画面には、tag:GetResources と対応する AWS サービスの読み取り専用 Describe も必要である。管理インターフェースから AWS リソースの起動・停止は行わない。
