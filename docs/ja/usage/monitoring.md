# ログ・メトリクス・通知

本ドキュメントは、reconciler の処理結果、リソースのエラー、および成功した操作を確認する方法を説明する。

## 設定と現在状態の確認

保存されたグループ設定と AWS の現在状態は、CLI の show または Web コンソールのグループ詳細で確認する。ECS では config がタグから得た設定、live が AWS の観測値である。状態取得に失敗したリソースは live_error を確認する。

show の成功だけでは、全リソースが要求どおりになったことを意味しない。確認用コマンドを、以下に示す。

```console
cheapskate-cli -table cheapskate -output json show --group dev
```

## 構造化ログ

reconciler と Web コンソールは構造化ログを stderr に出力する。reconciler の主なレコードを、以下に示す。

| レコード | 内容 |
| --- | --- |
| summary | 状態確認に到達したリソース数、成功した操作数、グループ・リソースのエラー数 |
| action | 成功した起動・停止または ECS 設定の修復 |
| group-error | グループ設定のエラー |
| resource-error | 個別リソースの状態確認・操作エラー |

該当するレコードには group、resource_id、desired、action、error を含む。DynamoDB に操作・エラー履歴を保存しないため、継続的な記録にはログを使用する。

全グループとタグ付きリソースの読み込みを完了したサイクルは、最後に summary を出力する。全体の読み込みに失敗した場合は summary を出力せず、Lambda 呼び出しがエラーとなる。個別グループやリソースの失敗では Lambda 呼び出し自体は成功するため、summary のエラー数も確認すること。

## CloudWatch メトリクス

METRICS_ENABLED=true の場合、METRICS_NAMESPACE に Embedded Metric Format でカスタムメトリクスを出力する。既定では無効である。出力する値を、以下に示す。

| メトリクス | 値 |
| --- | --- |
| ReconciledResources | 状態確認に到達した一意のリソース数 |
| ReconcileActions | 成功した操作数 |
| ReconcileErrors | グループとリソースのエラー数 |
| ReconcileAborted | 全体を中断したサイクルは 1、それ以外は 0 |

全体の読み込みに成功したサイクルは 4 個を出力する。読み込みに失敗したサイクルは ReconcileAborted=1 だけを出力する。単位は Count であり、dimension は付けない。

ReconcileErrors と ReconcileAborted はエラー検知に使用できる。CloudWatch Logs、alarm、および dashboard の作成と保持期間の設定は別途行う。

## SNS 通知

NOTIFICATION_TOPIC_ARN を指定すると、起動・停止または ECS 設定の修復に成功した後に通知する。成功は設定要求が受け付けられたことを表し、タスクやインスタンスの遷移完了を表さない。

通知失敗は同じ実行内で 1 回再試行する。再試行も失敗した場合はログへ記録し、成功したリソース操作を取り消さない。エラー通知と復旧通知は送信しない。同時実行や再試行によって通知が重複する場合があり、応答喪失などによって欠落する場合もある。

SNS topic と subscription は別途作成し、reconciler に sns:Publish を許可する。変数の設定は、[実行時設定](config.md#reconciler)を参照。
