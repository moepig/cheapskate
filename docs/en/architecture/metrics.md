# Metrics

## How metrics are emitted

The reconciler never calls `PutMetricData`. It writes EMF (CloudWatch Embedded Metric Format) log lines to stderr, and CloudWatch Logs ingests them and produces the metrics.

This way there is no added latency, throttling, or retrying from an API call, the execution role needs no `cloudwatch:PutMetricData`, and no resource beyond the Lambda's existing log group has to be created.

The implementation lives in `internal/aws/cloudwatch`; `internal/app` knows nothing of metrics. Emission happens in `cmd/reconciler`, once it has the `reconcile.Summary`.

## The metrics emitted

Custom metrics are disabled by default and are emitted only when `METRICS_ENABLED=true` is explicit. The namespace is `METRICS_NAMESPACE` (default `cheapskate`), there are no dimensions, and every unit is Count. The metrics emitted are given below.

| Metric | Meaning | When emitted |
| --- | --- | --- |
| `ReconciledResources` | Resources processed in the cycle | Only on a cycle that ran to completion |
| `ReconcileActions` | Starts and stops performed | Only on a cycle that ran to completion |
| `ReconcileErrors` | Per-resource and per-group failures | Only on a cycle that ran to completion |
| `ReconcileAborted` | `1` when the cycle never got going, `0` when it completed | Every cycle |

`ReconcileAborted` alone is emitted as `0` on completion too. Emitting only one side would leave alarms permanently in insufficient-data, with no way to tell trouble from silence.

When a cycle is cut short, the other three data points do not exist. It is not that the counts were zero, but that nothing was ever reached to count.

If the Lambda times out or panics, the process dies and no metric is emitted at all.

## Why there are no dimensions

Making the group name a dimension would grow the number of custom metrics in proportion to the number of groups. cheapskate aims to keep its own cost under one dollar a month and therefore does not adopt a design where the metric bill outgrows it as groups are added.

Per-group information lives in `last_error` on `status#group#<name>`, in the SNS notification subject, and in the `group` log attribute. The metrics' job ends at detecting that something is wrong; locating it belongs to those.

## Division of labour with the built-in Lambda metrics

What the built-in metrics and the EMF metrics each capture is collected below.

| Metric | What it captures |
| --- | --- |
| `Errors` (built-in) | Cycle-wide failures such as malformed input, an initial read failure, timeout, or panic |
| `Duration` (built-in) | How long one cycle takes |
| `Throttles` (built-in) | Invocations backing up against the reserved concurrency of 1 |
| `ReconcileErrors` | The number of per-resource and per-group failures. Emitted only when enabled |
| `ReconcileActions` | Starts and stops happening. Permanently 0 means the configuration is not taking effect |
| `ReconciledResources` | How the number of managed resources moves over time |

The handler returns success even when individual resources or groups fail, preventing EventBridge from retrying the entire cycle. Status, SNS, logs, and the enabled `ReconcileErrors` metric expose those failures.

## Kinds of failure and where they show up

Which observation paths each kind of failure reaches are collected below.

| Failure | `Errors` | `ReconcileErrors` | `ReconcileAborted` | SNS | `status#` | Log |
| --- | :---: | :---: | :---: | :-: | :---: | :--: |
| A failed Stop/Start or Describe | — | ✓ | 0 | ✓ | ✓ | ✓ |
| An invalid cron or timezone, a discovery failure | — | ✓ | 0 | ✓ | ✓ | ✓ |
| A selector collision | — | ✓ | 0 | ✓ | ✓ | ✓ |
| A malformed payload, a failed initial Query or BatchGetItem | ✓ | — | 1 | — | — | ✓ |
| A Lambda timeout or panic | ✓ | — | — | — | — | partial |
| A failed SNS Publish | — | — | 0 | awaiting retry | ✓ | ✓ |
| A failed pending/completion status write | — | ✓ | 0 | depends | depends | ✓ |
| A resource stuck mid-transition | — | — | 0 | — | ✓ (`transitioning_since`) | ✓ |

When an action notification fails, `notification_pending` remains and the next cycle sends it again with the same `operation_id`. If recording pending fails, no AWS action runs. If completion recording fails after the AWS action, pending remains and the next cycle confirms completion from the AWS observation instead of sending the same action again. Diagnosis catches transitions that never end ([overview.md](overview.md)).

## Disabling

`METRICS_ENABLED` defaults to `false`, which writes no EMF lines. Set it to `true` only in environments that need the metrics.

Enablement (`METRICS_ENABLED`) and the namespace (`METRICS_NAMESPACE`) are separate variables. This avoids overloading an empty namespace with enablement semantics and leaves the billed emission as an explicit opt-in.

A value of `METRICS_ENABLED` that cannot be interpreted fails startup rather than falling back to the default. Reading a typo as enabled would keep charges running after they were supposedly turned off, leaving the bill as the only way to notice. While disabled, one `metrics-disabled` log line is written per cold start.

The enabled/disabled decision belongs to the `cloudwatch.Emitter` type itself, so no caller writes the condition. There is more than one emission site, and repeating the same condition at each admits omitting one.

Disabling loses the counts and the trends, nothing else. What remains and what is lost are collected below.

| Kept while disabled | Lost while disabled |
| --- | --- |
| The built-in `Errors` / `Duration` / `Throttles` | `ReconcileErrors` (per-resource and per-group failure count) |
| SNS notifications (actions, failures, recoveries) | `ReconcileActions` (actions happening) |
| `last_error` on `status#` | `ReconciledResources` (the trend in managed resources) |
| The log | `ReconcileAborted` (detecting that invocation stopped) |

With custom metrics disabled, per-resource and per-group failures do not appear in the built-in `Errors` metric. Proactive detection requires SNS, status/log monitoring, or enabling the custom metrics.

## Cost

Metrics produced from EMF are custom metrics and are billed by CloudWatch. The breakdown is given below.

| Item | Count | Approximate cost (Tokyo region) |
| --- | --- | --- |
| Custom metrics | 4 | about $0.30 each per month = about $1.20/month |
| Alarms (standard resolution) | 1–3 | $0.10 each per month |
| EMF log ingestion | a few lines per cycle | Only what the Lambda's existing log group already costs |

> [!IMPORTANT]
> Enabling metrics can cost more per month than the control plane itself (Lambda + on-demand DynamoDB). The statement that cheapskate costs under a dollar a month covers compute and storage and does not include observability.

Since there are no dimensions, adding groups or resources never adds to these four metrics.

## Running locally

Running locally still writes the EMF lines to stderr, but with no CloudWatch Logs to ingest them they become no metrics and simply read as one more line of JSON log.

The tests in `internal/aws/cloudwatch` check the structure of the `_aws` block itself. If the shape is wrong, CloudWatch produces no metric and ignores the line silently rather than raising an error, so confirming that a log line was written proves nothing.
