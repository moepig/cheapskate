# AWS リソースと IAM

本ページではcheapskate の配置に必要な AWS リソースを示す。IAM 権限は[IAM 権限](iam.md)を参照。

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

cheapskate はこれらのリソースを自動作成しない。配置手順は[セットアップ](setup.md)を参照。

## ポリシー例の前提

IAM 権限の説明は[ポリシー例の前提](iam.md#ポリシー例の前提)を参照。

## アクションごとのリソースと条件

IAM 権限の説明は[アクションごとのリソースと条件](iam.md#アクションごとのリソースと条件)を参照。

## 共通のリソース参照権限

IAM 権限の説明は[共通のリソース参照権限](iam.md#共通のリソース参照権限)を参照。

## reconciler の権限

IAM 権限の説明は[reconciler の権限](iam.md#reconciler-の権限)を参照。

### DynamoDB

IAM 権限の説明は[DynamoDB](iam.md#dynamodb)を参照。

### RDS と EC2

IAM 権限の説明は[RDS と EC2](iam.md#rds-と-ec2)を参照。

### ECS と Application Auto Scaling

IAM 権限の説明は[ECS と Application Auto Scaling](iam.md#ecs-と-application-auto-scaling)を参照。

## CLI と Web コンソールの権限

IAM 権限の説明は[CLI と Web コンソールの権限](iam.md#cli-と-web-コンソールの権限)を参照。

## Lambda のログ配信

IAM 権限の説明は[Lambda のログ配信](iam.md#lambda-のログ配信)を参照。

## SNS 通知と暗号化の追加権限

IAM 権限の説明は[SNS 通知と暗号化の追加権限](iam.md#sns-通知と暗号化の追加権限)を参照。

### SNS 通知

IAM 権限の説明は[SNS 通知](iam.md#sns-通知)を参照。

### SNS の暗号化

IAM 権限の説明は[SNS の暗号化](iam.md#sns-の暗号化)を参照。

### 暗号化した EBS ボリュームの起動

IAM 権限の説明は[暗号化した EBS ボリュームの起動](iam.md#暗号化した-ebs-ボリュームの起動)を参照。

## 実行ロールの信頼と定期呼び出し

IAM 権限の説明は[実行ロールの信頼と定期呼び出し](iam.md#実行ロールの信頼と定期呼び出し)を参照。
