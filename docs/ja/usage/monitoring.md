# ログ・メトリクス・通知

本ドキュメントは、reconciler の処理結果、リソースのエラー、および成功した操作を確認する方法を説明する。

## 設定と現在状態の確認

保存されたグループ設定と AWS の現在状態は、CLI の show または Web コンソールのグループ詳細で確認する。show の JSON 出力では、resources の各要素の config がタグから得た設定、live が AWS の観測値である。状態取得に失敗したリソースは、同じ要素の live_error を確認する。

show の成功だけでは、全リソースが要求どおりになったことを意味しない。確認用コマンドを、以下に示す。

```console
cheapskate-cli -table cheapskate -output json show --group dev
```

## 構造化ログ

reconciler と Web コンソールは構造化ログを stderr に出力する。reconciler の主なレコードを、以下に示す。

| レコード | レベル | 内容 |
| --- | --- | --- |
| reconcile-started / reconcile-finished | debug | サイクル全体の開始・終了 |
| load-groups-started / load-groups-finished | debug | グループ設定読み込みの開始・終了 |
| resolve-desired-started / resolve-desired-finished | debug | 要求状態解決の開始・終了 |
| discover-resources-started / discover-resources-finished | debug | リソース検索の開始・終了 |
| reconcile-resources-started / reconcile-resources-finished | debug | リソース処理全体の開始・終了 |
| describe-resource-started / describe-resource-finished | debug | 操作前の状態取得の開始・終了 |
| decide-action-started / decide-action-finished | debug | 操作判定の開始・終了 |
| apply-action-started / apply-action-finished | debug | 操作要求の開始・終了 |
| notify-action-started / notify-action-finished | debug | 通知の開始・終了 |
| resource-discovered | debug | 検索で検出されたリソースの group、resource_id、resource_arn |
| summary | info | 状態確認に到達したリソース数、成功した操作数、グループ・リソースのエラー数 |
| action-started | info | 起動・停止または ECS 設定の修復を要求する直前のリソースと状態 |
| action-finished | info | 操作の成否、期待状態、および操作前の観測状態 |
| resource-unchanged | info | 変更不要と判定したリソースの期待状態、観測状態、および理由 |
| resource-skipped | info | disabled、transitioning、not-found によって処理を見送ったリソースと状態 |
| group-error | error | グループ設定のエラー |
| resource-error | error | 個別リソースの状態取得・操作エラー |

LOG_LEVEL=debug を指定すると、debug レベルのレコードも出力する。各処理の終了には success と、失敗時の error を含む。通知は試行ごとに attempt を含む。リソース検索の終了には検出件数 resources を含み、0 件の場合も記録する。resource-discovered は ARN 順に出力し、無効なグループや disabled のために処理対象から外れるリソースも含む。

リソースごとの info レコードは、処理の進行に合わせて逐次出力する。操作結果の主要フィールドを、以下に示す。

| フィールド | 内容 |
| --- | --- |
| group | 所属グループ |
| resource_id / resource_arn | 対象リソースの識別子 / ARN |
| desired | 期待する状態。running、stopped。disabled は空文字列 |
| action | 要求した操作。start、stop |
| observed | 操作判定用の共通状態。running、stopped、transitioning、not-found。disabled は null |
| aws_state | AWS から取得したステータス、または ECS のタスク数・スケーリング設定 |
| reason | 変更不要または処理を見送った理由。desired-state-matched、disabled、transitioning、not-found |
| success | 操作要求の成否。状態遷移の完了を表さない |
| error | 操作要求のエラー |

observed は AWS の観測値をリソース種別に共通する状態へ分類した値であり、aws_state は分類元のステータスやタスク数・スケーリング設定を表す。いずれも操作判定時の値である。action-finished は操作要求の成否を記録し、ログ出力のための操作後の再取得は行わない。操作後の状態は次のサイクル、CLI の show、または Web コンソールで確認する。

resource-unchanged の reason=desired-state-matched は、観測状態が期待状態と一致し、設定の修復も不要であることを表す。resource-skipped の reason は、disabled、transitioning、not-found のいずれかである。

AWS サービスとリソース種別は resource_id または resource_arn から判別する。

DynamoDB に操作・エラー履歴を保存しないため、継続的な記録にはログを使用する。

全グループとタグ付きリソースの読み込みを完了したサイクルは、リソース処理後に summary を出力する。全体の読み込みに失敗した場合は summary を出力せず、Lambda 呼び出しがエラーとなる。個別グループやリソースの失敗では Lambda 呼び出し自体は成功するため、summary のエラー数も確認すること。

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
