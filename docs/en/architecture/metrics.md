# Metrics

Custom metrics are disabled by default. When `METRICS_ENABLED=true`, the reconciler emits Embedded Metric Format records in `METRICS_NAMESPACE`.

| Metric | Value |
| --- | --- |
| `ReconciledResources` | Unique resources that reached Describe |
| `ReconcileActions` | Successful Start and Stop calls |
| `ReconcileErrors` | Group and resource errors |
| `ReconcileAborted` | `1` for a globally aborted cycle, otherwise `0` |

Enabling metrics does not provision CloudWatch resources.
