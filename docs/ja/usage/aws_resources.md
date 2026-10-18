# AWS リソースと IAM

本ページでは cheapskate の配置に必要な AWS リソースを示す。権限の設定は[IAM 権限](iam.md)を参照。

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

## IAM 権限

権限の対象から、対応する設定とポリシー例を参照する。

| 権限の対象 | 内容 |
| --- | --- |
| <a id="ポリシー例の前提"></a>[ポリシー例の前提](iam.md#ポリシー例の前提) | 対象範囲と置換変数 |
| <a id="アクションごとのリソースと条件"></a>[アクションごとのリソースと条件](iam.md#アクションごとのリソースと条件) | アクション、Resource、Condition の対応 |
| <a id="共通のリソース参照権限"></a>[共通のリソース参照権限](iam.md#共通のリソース参照権限) | reconciler と管理インターフェースの参照権限 |
| <a id="reconciler-の権限"></a>[reconciler の権限](iam.md#reconciler-の権限) | リソースの起動・停止権限 |
| <a id="dynamodb"></a>[DynamoDB](iam.md#dynamodb) | reconciler の CONFIG パーティションへの Query |
| <a id="rds-と-ec2"></a>[RDS と EC2](iam.md#rds-と-ec2) | reconciler の ARN・所属タグ条件 |
| <a id="ecs-と-application-auto-scaling"></a>[ECS と Application Auto Scaling](iam.md#ecs-と-application-auto-scaling) | 既存 scalable target の更新と ARN の確認 |
| <a id="cli-と-web-コンソールの権限"></a>[CLI と Web コンソールの権限](iam.md#cli-と-web-コンソールの権限) | グループ設定の読み書き |
| <a id="lambda-のログ配信"></a>[Lambda のログ配信](iam.md#lambda-のログ配信) | ログストリームの作成とログ送信 |
| <a id="sns-通知と暗号化の追加権限"></a>[SNS 通知と暗号化の追加権限](iam.md#sns-通知と暗号化の追加権限) | SNS 通知と暗号化を使う場合の追加権限 |
| <a id="sns-通知"></a>[SNS 通知](iam.md#sns-通知) | SNS topic への発行 |
| <a id="sns-の暗号化"></a>[SNS の暗号化](iam.md#sns-の暗号化) | 暗号化した topic の KMS 権限 |
| <a id="暗号化した-ebs-ボリュームの起動"></a>[暗号化した EBS ボリュームの起動](iam.md#暗号化した-ebs-ボリュームの起動) | EBS ボリュームの KMS grant 作成 |
| <a id="実行ロールの信頼と定期呼び出し"></a>[実行ロールの信頼と定期呼び出し](iam.md#実行ロールの信頼と定期呼び出し) | 実行ロールの信頼と定期呼び出し権限 |
