# Metrics

Custom metrics are disabled by default. When `METRICS_ENABLED=true`, the reconciler emits Embedded Metric Format records in `METRICS_NAMESPACE`.

| Metric | Value |
| --- | --- |
| `ReconciledResources` | Unique resources that reached Describe |
| `ReconcileActions` | Successful Start and Stop calls |
| `ReconcileErrors` | Group and resource errors |
| `ReconcileAborted` | `1` for a globally aborted cycle, otherwise `0` |

Enabling metrics does not provision CloudWatch resources.

Cycles that complete both global reads emit all four metrics. A configuration Query or global discovery failure emits only ReconcileAborted=1; the count metrics are omitted. All metrics use Count units and no dimensions.
