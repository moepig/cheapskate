# AWS リソースと IAM

reconciler に必要なアクセス権限を次の表に示す。

| サービス | アクセス |
| --- | --- |
| DynamoDB | `CONFIG` パーティションに限定した `Query` |
| Resource Groups Tagging API | `GetResources` |
| RDS | DB インスタンスと DB クラスターの Describe、Start、Stop |
| ECS | `DescribeServices` と `UpdateService` |
| Application Auto Scaling | ECS scalable target の Describe と Register |
| EC2 | インスタンスの Describe、Start、Stop |
| SNS | 設定した topic への任意の `Publish` |
| CloudWatch Logs | Lambda のログ配信 |

CLI と Web コンソールには、`CONFIG` パーティションに対する `Query`、`GetItem`、`PutItem`、`UpdateItem`、および `DeleteItem` が必要である。`show` とグループ詳細表示には `GetResources` と読み取り専用の Describe も必要になる。

cheapskate は log group、alarm、dashboard、および topic を作成しない。
