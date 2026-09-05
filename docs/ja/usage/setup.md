# セットアップ

## 1. DynamoDB table の作成

String 型のパーティションキー `pk` と String 型のソートキー `sk` を持つ table を作成する。用途に応じて on-demand または provisioned capacity を選択する。TTL とセカンダリインデックスは設定しない。

```console
aws dynamodb create-table \
  --table-name cheapskate \
  --billing-mode PAY_PER_REQUEST \
  --attribute-definitions AttributeName=pk,AttributeType=S AttributeName=sk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE
```

## 2. reconciler のデプロイ

reconciler container image を Lambda function としてデプロイする。`STATE_TABLE_NAME` と、必要に応じて[実行時設定](config.md)の変数を設定する。想定する最大タグ付きリソース数で full reconcile を試験し、余裕を持って完了できる timeout を選択する。Lambda timeout の最大値は 900 秒である。

5 分ごとに Lambda を呼び出す EventBridge Scheduler または EventBridge rule を 1 個作成する。すべての invocation が full reconcile を行うため、payload の内容は任意である。

reconciler の DynamoDB policy には、`CONFIG` パーティションに限定した強い整合性の Query だけが必要である。

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

このほかに、`tag:GetResources`、有効にする RDS、ECS、Application Auto Scaling、および EC2 adapter の読み取りと変更、CloudWatch Logs への配信、ならびに任意の通知先への `sns:Publish` を付与する。詳細は、[AWS リソースと IAM](../architecture/aws_resources.md)を参照すること。

## 3. 管理インターフェースの導入

信頼できる運用環境で `cheapskate-cli` を実行するか、任意の Web console image を Lambda Web Adapter と認証済み API Gateway などの ingress の背後へデプロイする。

CLI と Web コンソールには、次の DynamoDB action が必要である。

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

`show` と詳細画面には、`tag:GetResources` と対応 service の読み取り専用 Describe も付与する。グループアイテムを IaC から直接管理してはならない。アイテム全体の検証と条件付き書き込みを行うため、CLI または Web コンソールを使用する。

## 4. タグとグループの設定

各リソースへ `cheapskate:group=<グループ>` を設定し、ECS には必要な復元タグも設定する。次の例は schedule を作成する。

```console
cheapskate-cli -table cheapskate schedule --group dev \
  -start '0 8 * * 1-5' -stop '0 20 * * 1-5'
```

reconciler を 1 回手動で呼び出し、ログと `show` の現在状態を確認してから 5 分間隔の呼び出しを有効にする。

cheapskate は CloudWatch log group、alarm、dashboard、SNS topic、および通知 subscription を作成しない。
