# Metrics

## How metrics are emitted

The reconciler never calls `PutMetricData`. It writes EMF (CloudWatch Embedded Metric Format) log lines to stderr, and CloudWatch Logs ingests them and produces the metrics.

This way there is no added latency, throttling, or retrying from an API call, the execution role needs no `cloudwatch:PutMetricData`, and no resource beyond the Lambda's existing log group has to be created.

The implementation lives in `internal/aws/cloudwatch`; `internal/app` knows nothing of metrics. Emission happens in `cmd/reconciler`, once it has the `reconcile.Summary`.

## The metrics emitted

The namespace is `METRICS_NAMESPACE` (default `cheapskate`), there are no dimensions, and every unit is Count. The metrics emitted are given below.

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
| `Errors` (built-in) | Trouble with the cycle as a whole, and any cycle with one or more per-resource failures |
| `Duration` (built-in) | How long one cycle takes |
| `Throttles` (built-in) | Invocations backing up against the reserved concurrency of 1 |
| `ReconcileErrors` | The number of failures; `Errors` only distinguishes 0 from 1 |
| `ReconcileActions` | Starts and stops happening. Permanently 0 means the configuration is not taking effect |
| `ReconciledResources` | How the number of managed resources moves over time |

`Errors` captures per-resource failures because a non-zero failure count makes the handler return an error (see the reconcile loop rules in [overview.md](overview.md)).

## Kinds of failure and where they show up

Which observation paths each kind of failure reaches are collected below.

| Failure | `Errors` | `ReconcileErrors` | `ReconcileAborted` | SNS | `status#` | Log |
| --- | :---: | :---: | :---: | :-: | :---: | :--: |
| A failed Stop/Start or Describe | ✓ | ✓ | 0 | ✓ | ✓ | ✓ |
| An invalid cron or timezone, a discovery failure | ✓ | ✓ | 0 | ✓ | ✓ | ✓ |
| A selector collision | ✓ | ✓ | 0 | ✓ | ✓ | ✓ |
| A malformed payload, a failed `Scan` | ✓ | — | 1 | — | — | ✓ |
| A Lambda timeout or panic | ✓ | — | — | — | — | partial |
| A failed SNS Publish, a failed `status#` write | — | — | 0 | — | — | ✓ |
| A resource stuck mid-transition | — | — | 0 | — | ✓ (`transitioning_since`) | ✓ |

Failures of the recording path and transitions that never end are deliberately absent everywhere, because in neither case did the operation itself fail. The log catches the former ([logging.md](logging.md)) and the diagnosis catches the latter ([overview.md](overview.md)).

## Disabling

`METRICS_ENABLED=false` stops the EMF lines from being written at all.

Enablement (`METRICS_ENABLED`) and the namespace (`METRICS_NAMESPACE`) are separate variables. Merging them would require a convention along the lines of "unset means enabled in the default namespace, empty string means disabled", making this the one variable where unset and empty differ in meaning.

A value of `METRICS_ENABLED` that cannot be interpreted fails startup rather than falling back to the default. Reading a typo as enabled would keep charges running after they were supposedly turned off, leaving the bill as the only way to notice. While disabled, one `metrics-disabled` log line is written per cold start.

The enabled/disabled decision belongs to the `cloudwatch.Emitter` type itself, so no caller writes the condition. There is more than one emission site, and repeating the same condition at each leaves room to forget one.

Disabling loses the counts and the trends, nothing else. What remains and what is lost are collected below.

| Kept while disabled | Lost while disabled |
| --- | --- |
| The built-in `Errors` / `Duration` / `Throttles` | `ReconcileErrors` (the number of failures) |
| SNS notifications (actions, failures, recoveries) | `ReconcileActions` (actions happening) |
| `last_error` on `status#` | `ReconciledResources` (the trend in managed resources) |
| The log | `ReconcileAborted` (detecting that invocation stopped) |

Because per-resource failures ride on the built-in `Errors`, failures can still be noticed with metrics disabled.

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
