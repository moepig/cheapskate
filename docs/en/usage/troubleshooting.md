# Handling failures and recovering state

This document covers what to do when a reconcile fails partway, when a resource is left half-finished, and when unwanted records remain in the state table.

cheapskate is a convergence loop: a failure is retried by the next cycle doing the same work again, and most transient failures resolve with no intervention. What this document addresses is the events that do not resolve on their own, and how to tell them apart.

## Detecting failures

The paths through which a failure is detected, and what each yields, are collected below.

| Path | Information |
| --- | --- |
| SNS notifications | Actions performed, failures, and recoveries. The same error is not notified again while it persists |
| The Lambda `Errors` metric | Trouble with the cycle as a whole (a malformed payload, a failed initial Query or BatchGetItem, a timeout, a panic) |
| The `ReconcileErrors` / `ReconcileActions` / `ReconciledResources` / `ReconcileAborted` metrics | How the counts move when explicitly enabled |
| `last_error` on `status#` | The last error per resource. Read through `cheapskate-cli list` / `show` or the web console |
| `cheapskate-cli doctor` | Inconsistencies and leftover records in the state table |
| CloudWatch Logs | The failures that appear in none of the above |

`Errors` catches only a cycle-wide abort. Lambda returns success despite per-resource or per-group failures, so EventBridge does not retry the full reconcile for them. Detect those failures through SNS, status/log monitoring, or the explicitly enabled `ReconcileErrors` metric.

> [!WARNING]
> With none of SNS, status/log monitoring, or an enabled `ReconcileErrors` alarm, no proactive detection path exists even while every resource keeps failing. Provide at least one.

### Failures that appear in neither the metrics nor the notifications

A failed SNS Publish remains in the log and is retried once within the same processing run. A notification whose two attempts both fail is lost. A transition that never ends appears in neither metrics nor notifications, so `doctor` catches it.

### Configuring alarms

The following alarm catches cycle-wide failures.

```console
aws cloudwatch put-metric-alarm --alarm-name cheapskate-reconciler-errors \
  --namespace AWS/Lambda --metric-name Errors --statistic Sum \
  --dimensions Name=FunctionName,Value=cheapskate-reconciler \
  --period 300 --evaluation-periods 2 --threshold 0 --comparison-operator GreaterThanThreshold \
  --treat-missing-data notBreaching --alarm-actions <SNS topic ARN>
```

`evaluation-periods` is 2 to avoid alerting immediately on a transient infrastructure failure. Firing after one cycle would include events needing no intervention.

To watch per-resource and per-group failure counts, set `METRICS_ENABLED=true` and add the following alarm.

```console
aws cloudwatch put-metric-alarm --alarm-name cheapskate-reconcile-errors \
  --namespace cheapskate --metric-name ReconcileErrors --statistic Maximum \
  --period 300 --evaluation-periods 2 --threshold 0 --comparison-operator GreaterThanThreshold \
  --treat-missing-data notBreaching --alarm-actions <SNS topic ARN>
```

`ReconciledResources`, `ReconcileActions`, and `ReconcileErrors` have no data point at all when the cycle never got going. Alarms on those three should use `treatMissingData=notBreaching`.

To detect invocation itself stopping, put an alarm with `treatMissingData=breaching` on `ReconcileAborted`. Invocation stopping breaks the stream of data points, which catches a halted scheduled trigger or a broken event rule.

## What half-finished work leaves behind

The events left behind when processing ends partway, and what each calls for, are collected below.

| Event | Resolves on its own? | What is needed |
| --- | --- | --- |
| The Lambda timed out partway through a group | Yes. The next cycle starts over | If it happens every time, raise the memory and the timeout |
| The initial Query or BatchGetItem failed and the cycle never got going | Yes. EventBridge retries it, and there is the next cycle | Nothing |
| A Stop/Start failed | Yes. The next cycle retries | For a permanent cause such as a missing permission, read `last_error` and fix it |
| Recording pending failed | No AWS action ran, so the next cycle retries | Fix the DynamoDB failure only if it persists |
| The action succeeded but writing completion status failed | Pending remains. The next cycle confirms completion from AWS state and does not send the action again | Nothing |
| Both Publish attempts for a notification failed | No. The notification is abandoned and AWS actions continue | Monitor `*-notify-abandoned` logs if needed |
| Stopping ECS failed before the desiredCount update | Yes. The scalable target rolls back to its original min/max automatically | Nothing. Only if the rollback failed too, see [ECS-specific notes](#ecs-specific-notes) |
| A resource is stuck mid-transition | No. It is skipped on every cycle | See [Resources stuck mid-transition](#resources-stuck-mid-transition) |
| A group was deleted but `override#` / `status#` remain | DynamoDB TTL deletes records with `expires_at` after expiry | Retry `remove` or use `doctor --prune` |
| A `status#` remains for a resource whose tag was removed | DynamoDB TTL deletes it after the retention period from its last update | Use `doctor --prune` for immediate deletion |
| Selectors collide and one group is being ignored | No | See [Selector collisions](#selector-collisions) |
| An ECS service was unmanaged while stopped | No. It stays at desiredCount 0 / Auto Scaling 0-0 | See [ECS-specific notes](#ecs-specific-notes) |

## Diagnosis with doctor

One command surfaces the inconsistencies in the state table and the records left behind by half-finished work. By default it only reads.

```console
cheapskate-cli doctor                      # diagnose only
cheapskate-cli doctor --prune              # delete orphaned records
cheapskate-cli doctor --stuck-after 2h     # change the limit for counting as transitioning (default 30m)
```

In the web console, the diagnostics page shows the same results and deletes orphaned records under the same conditions.

TTL deletion is asynchronous and can take up to 48 hours after expiry. Existing status items with no `expires_at` are ineligible for automatic deletion; use `doctor --prune` for orphaned items that will receive no further updates.

The `kind` values reported, and whether `--prune` acts on them, are collected below.

| `kind` | Meaning | Deleted by `--prune` |
| --- | --- | --- |
| `orphan-override` | An `override#` remains with no `group#` | Yes |
| `orphan-group-status` | A `status#group#` remains with no `group#` | Yes |
| `orphan-status` | A `status#` for a resource matching no group's selector | Yes |
| `corrupt-record` | A record that fails to read or validate | No |
| `config-error` | A registered configuration the reconciler cannot follow (pinned with no desired, and the like) | No |
| `discover-error` | The selector is valid but discovering the resources failed | No |
| `selector-overlap` | Several groups' selectors match the same resource | No |
| `stuck-transitioning` | Still transitioning beyond `--stuck-after` | No |

`--prune` deletes only records whose group or resource is proven absent by the table read and the discovery alone. It touches neither the configuration itself (`group#`), nor anything requiring human judgement, nor the AWS resources.

As a safeguard, a cycle in which even one discovery fails withholds the `orphan-status` verdict entirely, so that the audit record of a resource that merely could not be discovered is not deleted. In that case `blocked` carries the reason.

```console
$ cheapskate-cli doctor | jq '{blocked, counts}'
{
  "blocked": ["group \"dev\" could not be discovered (AccessDenied); its members are unknown"],
  "counts": {"discover-error": 1}
}
```

> [!IMPORTANT]
> Zero `orphan-status` findings while `blocked` is non-empty does not mean there are no orphaned records; it means no verdict was reached. Fix the cause and run it again.

Since each finding carries the raw DynamoDB key in `pk` and `sk`, deleting by hand without `--prune` is possible too.

```console
aws dynamodb delete-item --table-name <state-table-name> \
  --key "$(cheapskate-cli doctor | jq -c '{pk: {S: .findings[0].pk}, sk: {S: .findings[0].sk}}')"
```

## Emergency procedures

Any intervention takes effect on the next reconcile cycle (5 minutes by default). To go faster, invoke the reconciler by hand.

```console
aws lambda invoke --function-name cheapskate-reconciler --payload '{}' /dev/stdout
```

`{}` means a full reconcile. The JSON in the response is that cycle's `actions` and `errors`.

### Starting everything in a group

To start every resource in a group temporarily, register a time-limited override.

```console
cheapskate-cli override --group dev running -for 8h
```

> [!IMPORTANT]
> The order of operations is constrained. A group that has been `disable`d accepts no override, `disabled` being a stronger stop than an override. Having reached for `disable` first while handling an incident, return the group to `pin` or `schedule` before it can be started.

Coming back from `disable` means putting the mode back.

```console
cheapskate-cli pin --group dev running     # after a disable
```

Conversely, for work to be done by hand with cheapskate kept out of it, `disable` is the right tool. cheapskate stops consulting that group entirely, but it does not roll back the actions it already took: resources already stopped stay stopped.

### Deleting a group

To take a group out of the managed set, delete its configuration records.

```console
cheapskate-cli remove --group dev
```

Deletion goes `override#` → `status#group#` → `group#`, so a failure partway leaves the group itself in place and a retry can still reach it. Per-resource `status#` records remain immediately after deletion, but DynamoDB TTL removes them after the retention period. Use `doctor --prune` to remove them immediately.

Deletion never touches an AWS resource. Afterwards the resources stay exactly as cheapskate last left them.

> [!CAUTION]
> Unless leaving them stopped and unmanaged is the intent, start them with `override running` before `remove` and wait one cycle.

## ECS-specific notes

Stopping takes two steps — setting the Application Auto Scaling min/max to 0/0, then setting the desiredCount to 0 — and is not atomic.

If the second step fails, the scalable target rolls back to its original min/max automatically. The service stays running and remains able to scale out.

Only if the rollback fails too is `left clamped at 0/0`, along with the values to restore, recorded in `last_error`. A service in that state is running but cannot scale out, so restore it by hand.

```console
aws application-autoscaling register-scalable-target --service-namespace ecs \
  --resource-id service/dev-cluster/api --scalable-dimension ecs:service:DesiredCount \
  --min-capacity 2 --max-capacity 6
```

### Unmanaging a service while it is stopped

Removing the selector tag from a stopped service, or deleting the group, leaves that service at desiredCount 0 with Auto Scaling 0-0. cheapskate does not touch unmanaged resources, so no command exists to recover it. Restore it by hand with the command above and `aws ecs update-service --desired-count`.

> [!CAUTION]
> Take an ECS service out of management only after starting it.

### The size at start

The size at start is restored from the resource's own tags. With no tags it starts at desiredCount 1, so apply the tags before stopping it for the first time. For details, see the parameter tags in [resource_tag.md](resource_tag.md).

## Resources stuck mid-transition

Transitional states such as `starting`, `stopping`, `modifying`, and `backing-up` are skipped by the reconciler on every cycle. Being neither an error nor a notification, `transitioning_since` on `status#` is the only clue.

`transitioning_since` is written exactly once, on the cycle that first observes the transition, and disappears the moment a non-transitional state is observed. It can be read in the following places.

- `status.transitioning_since` in `cheapskate-cli show --group <name>`
- The group page of the web console
- `stuck-transitioning` in `cheapskate-cli doctor`

A value that has been there a long time most likely means something is happening that waiting will not resolve. For RDS, suspect a maintenance window, a backup, or a stop requested during snapshot creation; for ECS, a long deregistration delay or a task that will not stop. Nothing on the cheapskate side can help, so investigate directly through the AWS Management Console or the API. Once the transition resolves, the next cycle converges the resource automatically.

## Selector collisions

When the same resource matches the selectors of two or more groups, only the first group by name takes effect and the rest are ignored. Each ignored group records an error on its own `status#group#<name>`.

```console
$ cheapskate-cli doctor | jq -r '.findings[] | select(.kind == "selector-overlap") | .detail'
matched by 2 groups [a-first z-second]; only "a-first" takes effect, the rest are ignored
```

This does not resolve until one of the selectors is changed. One group's configuration is not being applied at all, so do not leave it standing.
