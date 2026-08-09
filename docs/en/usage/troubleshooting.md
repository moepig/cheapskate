# Handling failures and recovering state

This document covers what to do when a reconcile fails partway, when a resource is left half-finished, and when unwanted records remain in the state table.

cheapskate is a convergence loop: a failure is retried by the next cycle doing the same work again, and most transient failures resolve with no intervention. What this document addresses is the events that do not resolve on their own, and how to tell them apart.

## Detecting failures

The paths through which a failure is detected, and what each yields, are collected below.

| Path | Information |
| --- | --- |
| SNS notifications | Actions performed, failures, and recoveries. The same error is not notified again while it persists |
| The Lambda `Errors` metric | Trouble with the cycle as a whole (a malformed payload, a failed `Scan`, a timeout, a panic), and any cycle with one or more per-resource failures |
| The `ReconcileErrors` / `ReconcileActions` / `ReconciledResources` / `ReconcileAborted` metrics | How the counts move over time |
| `last_error` on `status#` | The last error per resource. Read through `cheapskate-cli list` / `show` or the web console |
| `cheapskate-cli doctor` | Inconsistencies and leftover records in the state table |
| CloudWatch Logs | The failures that appear in none of the above |

A single alarm on `Errors` catches both trouble with the cycle as a whole and per-resource failures. `Errors` does not distinguish the number of failures, though, so use `ReconcileErrors` to watch the counts.

> [!NOTE]
> When `Errors` fires, EventBridge's asynchronous retries run the same full reconcile up to two more times. Converged resources produce no action, and a persisting error falls under notification deduplication, so the retries add no notifications.

> [!WARNING]
> With no SNS topic configured, notification is a no-op. If no `Errors` alarm is configured either, no detection path exists at all, even while every resource keeps failing. Configure at least one of the topic and the alarm.

### Failures that appear in neither the metrics nor the notifications

Failures of the recording path (a failed SNS Publish or `status#` write) and transitions that never end appear in neither the metrics nor the notifications, since in both cases the operation itself succeeded. The log catches the former and `doctor` the latter.

### Configuring alarms

The minimum is the single alarm below. It catches both trouble with the cycle as a whole and per-resource failures.

```console
aws cloudwatch put-metric-alarm --alarm-name cheapskate-reconciler-errors \
  --namespace AWS/Lambda --metric-name Errors --statistic Sum \
  --dimensions Name=FunctionName,Value=cheapskate-reconciler \
  --period 300 --evaluation-periods 2 --threshold 0 --comparison-operator GreaterThanThreshold \
  --treat-missing-data notBreaching --alarm-actions <SNS topic ARN>
```

`evaluation-periods` is 2 because a transient failure converges by itself on the next cycle. Firing after a single cycle would put events needing no intervention into the notifications.

Watching the counts separately looks as follows.

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
| The `Scan` failed and the cycle never got going | Yes. There are retries and the next cycle | Nothing |
| A Stop/Start failed | Yes. The next cycle retries | For a permanent cause such as a missing permission, read `last_error` and fix it |
| The action succeeded but writing `status#` failed | Yes, though for a cycle or two a successful action carries a spurious error, and a recovery is notified afterwards | Nothing (accept it as notification noise) |
| Stopping ECS failed before the desiredCount update | Yes. The scalable target rolls back to its original min/max automatically | Nothing. Only if the rollback failed too, see [ECS-specific notes](#ecs-specific-notes) |
| A resource is stuck mid-transition | No. It is skipped on every cycle | See [Resources stuck mid-transition](#resources-stuck-mid-transition) |
| A group was deleted but `override#` / `status#` remain | No | `doctor --prune` |
| A `status#` remains for a resource whose tag was removed | No (with no effect on behaviour) | `doctor --prune` |
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

Since each finding carries the raw DynamoDB key in `pk`, deleting by hand without `--prune` is possible too.

```console
aws dynamodb delete-item --table-name <state-table-name> \
  --key "$(cheapskate-cli doctor | jq -c '{pk: {S: .findings[0].pk}}')"
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

Deletion goes `override#` → `status#group#` → `group#`, so a failure partway leaves the group itself in place and a retry can still reach it. The per-resource `status#` records remain; use `doctor --prune` to remove them.

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
