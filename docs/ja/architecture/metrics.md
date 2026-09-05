# メトリクス

カスタムメトリクスは既定で無効である。`METRICS_ENABLED=true` の場合、reconciler は `METRICS_NAMESPACE` に Embedded Metric Format のレコードを出力する。

出力するメトリクスを次の表に示す。

| メトリクス | 値 |
| --- | --- |
| `ReconciledResources` | Describe まで到達した一意のリソース数 |
| `ReconcileActions` | 成功した Start と Stop の呼び出し数 |
| `ReconcileErrors` | グループとリソースのエラー数 |
| `ReconcileAborted` | 全体を中断したサイクルは `1`、それ以外は `0` |

メトリクスを有効にしても CloudWatch リソースは作成しない。
