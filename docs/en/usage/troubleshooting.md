# Troubleshooting and state recovery

What to do when a reconcile fails partway, a resource is left mid-transition, or the state table accumulates leftovers. For day-to-day configuration see [operations.md](operations.md).

cheapskate is a converging loop: **a failure is retried in full by the next 5-minute cycle**. Most transient failures fix themselves. This page is about the ones that do not, and how to tell them apart.

## 1. Noticing

| Signal | What it tells you |
|---|---|
| SNS notifications | Actions taken, failed, and recovered. An unchanged error is not re-notified (B-3) |
| Lambda `Errors` metric | Cycle-wide failures (bad payload, `Scan` failure, timeout, panic) **and** any cycle with at least one resource-level failure |
| EMF metrics (when `METRICS_ENABLED` is left at its default of true) | `ReconcileErrors` / `ReconcileActions` / `ReconciledResources` / `ReconcileAborted` counts over time |
| `last_error` on `status#` items | Per-resource last error, via `cheapskate-cli list` / `show` and the web console |
| `cheapskate-cli doctor` | State-table inconsistencies and leftover records (see 3 below) |

Resource-level failures (`AccessDenied`, a bad cron, a Tagging API failure) used to be invisible to the Lambda `Errors` metric, leaving SNS as the only signal. The handler now returns an error carrying the failure count, so **a single alarm on `Errors` covers both layers**. `Errors` cannot tell you *how many* failed, though — use the EMF metrics for that.

An `Errors` result makes EventBridge retry the same full reconcile up to twice more. Converged resources produce no actions and continuing errors hit the B-3 dedupe, so retries never add notifications.

With no SNS topic configured, notifications are a no-op. Without an `Errors` alarm as well, every resource could be failing and nothing would ever fire. **Set up at least one of the two** ([setup.md §3](setup.md#3-sns-topic-optional)).

## 2. What a half-finished cycle leaves behind

| Situation | Self-healing? | What you do |
|---|---|---|
| Lambda timed out midway through the groups | Yes — the next cycle starts over | Raise memory/timeout if it happens every cycle |
| `Scan` failed, so the cycle never got going | Yes — EventBridge retries, and the next cycle runs anyway | Nothing |
| A Stop/Start call failed | Yes — retried next cycle | If the cause is permanent (missing permission), read `last_error` and fix it |
| The action succeeded but writing `status#` failed | Yes — but for a cycle or two a successful action carries a bogus error, followed by a "recovered" notification | Nothing (accept the notification noise) |
| An ECS stop failed after the scalable target was zeroed | Yes — the scalable target is rolled back to its original min/max automatically | Only if the rollback itself failed: restore by hand using the values in the error |
| A resource is stuck mid-transition (`stopping`, …) | **No** — silently skipped every cycle | Find it with `doctor`, then investigate on the AWS side |
| A group was deleted but its `override#` / `status#` remain | **No** | `doctor --prune` |
| A `status#` remains for an untagged resource | **No** (harmless, though) | `doctor --prune` |
| Overlapping selectors mean one group is ignored | **No** | Find it with `doctor`, fix one of the selectors |
| An ECS service was detached while stopped | **No** — left at desiredCount 0 / AAS 0-0 | See 5 below |

## 3. `cheapskate-cli doctor`

One command to surface state-table inconsistencies and the garbage left by half-finished work. Read-only by default.

```console
cheapskate-cli doctor                      # diagnose only
cheapskate-cli doctor --prune              # delete the orphaned records
cheapskate-cli doctor --stuck-after 2h     # change the stuck threshold (default 30m)
```

The web console exposes the same thing behind the **diagnostics** link in the header, including prune under the same rules.

| `kind` | Meaning | Removed by `--prune`? |
|---|---|---|
| `orphan-override` | An `override#` whose `group#` does not exist | Yes |
| `orphan-group-status` | A `status#group#` whose `group#` does not exist | Yes |
| `orphan-status` | A resource `status#` matching no group's selector | Yes |
| `corrupt-record` | A record that fails to unmarshal or validate | No |
| `config-error` | Registered, but the reconciler cannot act on it (e.g. pinned without a desired state) | No |
| `discover-error` | The selector is valid but `tag:GetResources` failed | No |
| `selector-overlap` | Several groups' selectors claim the same resource | No |
| `stuck-transitioning` | Transitioning for longer than `--stuck-after` | No |

`--prune` only deletes records whose group or resource is **provably absent from the table scan and the discovery pass**. It never touches configuration (`group#`), never touches anything needing a human decision (overlaps, config errors, stuck transitions), and never touches AWS resources.

**The important safety property**: if even one discovery call fails, the `orphan-status` check is skipped entirely, so a resource that is merely invisible for a moment does not lose its audit trail. The reason lands in `blocked`:

```console
$ cheapskate-cli doctor | jq '{blocked, counts}'
{
  "blocked": ["group \"dev\" could not be discovered (AccessDenied); its members are unknown"],
  "counts": {"discover-error": 1}
}
```

When `blocked` is non-empty, zero `orphan-status` findings means **"not checked"**, not "none found". Fix the cause and run it again.

Every finding carries the raw DynamoDB key in `pk`, so you can delete by hand instead of using `--prune`.

## 4. Emergency procedures

Every intervention lands on the next reconcile cycle (5 minutes by default). To skip the wait, invoke the reconciler directly:

```console
aws lambda invoke --function-name cheapskate-reconciler --payload '{}' /dev/stdout
```

`{}` means a full reconcile. The response JSON is that cycle's `actions` and `errors`.

### "Just start everything"

```console
cheapskate-cli override --group dev running -for 8h
```

**Ordering trap**: a disabled group rejects overrides — `disabled` is a stronger stop than any override. If your first move during an incident was `disable`, you must go back through `pin` or `schedule` before you can start anything:

```console
cheapskate-cli pin --group dev running
```

Conversely, `disable` is exactly right for "keep cheapskate's hands off while I work manually". It stops all management but **does not undo anything already done** — stopped resources stay stopped.

### Removing a group

```console
cheapskate-cli remove --group dev
```

Deletion order is `override#` → `status#group#` → `group#`, so a failure partway leaves the group itself reachable for a retry. Per-resource `status#` items survive; clear them with `doctor --prune`.

AWS resources are never touched, which means **they stay exactly as cheapskate last left them**. If you do not intend to leave them stopped, `override running` and wait one cycle before removing the group.

## 5. ECS-specific notes

Stopping is two steps — zero the Application Auto Scaling min/max, then set desiredCount to 0 — and is not atomic. The order is required: a scaling policy would otherwise undo the desiredCount change.

- If the second step fails, the scalable target is **rolled back to its original min/max automatically**. The service stays up and can still scale out.
- Only if the rollback also fails does `last_error` say `left clamped at 0/0`, along with the values to restore. Such a service is running but cannot scale out, so fix it by hand:

```console
aws application-autoscaling register-scalable-target --service-namespace ecs \
  --resource-id service/dev-cluster/api --scalable-dimension ecs:service:DesiredCount \
  --min-capacity 2 --max-capacity 6
```

- **Untagging (or removing the group of) a stopped service leaves it at desiredCount 0 and AAS 0-0.** cheapskate has no command to restore it, because it does not touch unmanaged resources. Recover with the command above plus `aws ecs update-service --desired-count`. Start the service before detaching it.
- Start-up sizing comes from tags on the service (`cheapskate/desired-count`, `cheapskate/scaling-min`, `cheapskate/scaling-max`), not from anything saved at stop time. Without them a service comes back at desiredCount 1. **Tag it before the first stop.**

## 6. Resources stuck mid-transition

Transitional states (`starting`, `stopping`, `modifying`, `backing-up`) are skipped every cycle. That is neither an error nor a notification, so `transitioning_since` on the `status#` item is the only trace.

- Written once, on the cycle that first observes the transition; cleared as soon as a settled state is observed.
- Visible as `status.transitioning_since` in `cheapskate-cli show --group`, on the group page in the web console, and as `stuck-transitioning` in `doctor`.

A long-lived value means something that waiting will not fix. For RDS, suspect a maintenance window, a backup, or a stop requested during snapshot creation; for ECS, a long deregistration delay or a task that will not stop. cheapskate can do nothing here — investigate through the AWS console or API. Once the transition clears, the next cycle converges it automatically.

## 7. Overlapping selectors

When two or more groups' selectors match the same resource, **only the first group by name takes effect and the rest are silently ignored**. The losing group records the error against its own `status#group#<name>` (never against the resource's `status#`, which belongs to the owning group).

```console
$ cheapskate-cli doctor | jq -r '.findings[] | select(.kind == "selector-overlap") | .detail'
matched by 2 groups [a-first z-second]; only "a-first" takes effect, the rest are ignored
```

This does not resolve on its own, and it means one group's entire configuration is inert. Fix one of the selectors.
