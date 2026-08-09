# Events and invocation paths

## Invocation paths

Three paths start the reconciler. The trigger and payload of each are given below.

| Kind | Trigger | Payload |
|---|---|---|
| Scheduled | EventBridge Scheduler, `rate(5 minutes)` | `{}` |
| RDS auto-start | Lambda invoked from an EventBridge rule (`aws.rds` events) | The EventBridge event |
| Manual | `aws lambda invoke` and the like | `{}` |

## Ruling out concurrent runs

The Lambda function running the reconciler must have its reserved concurrency set to 1, so that the invocation paths cannot run concurrently.

## RDS auto-start events

An RDS instance or Aurora cluster may stay stopped for at most seven days, after which AWS starts it automatically. To bring an automatically started instance or cluster back in line with its schedule, the reconciler runs from the EventBridge event emitted at start.

> [!NOTE]
> The same event is emitted when a resource is started deliberately. Not every event therefore calls for an action on a resource.

### Events handled

The event IDs subscribed to are given below.

| EventID | Meaning |
|---|---|
| `RDS-EVENT-0088` | Instance start completed |
| `RDS-EVENT-0151` | Cluster start completed |

## SNS notifications

Notifications happen only when the `NOTIFICATION_TOPIC_ARN` environment variable is set.

### Kinds of notification

The subject is `[cheapskate] <kind>: <group name>/<resource ID>`, and the body is a single JSON object.

The kinds of notification, their bodies, and what prompts them are collected below.

| Kind | Subject's kind part | Body attributes | Prompted by |
|---|---|---|---|
| Action | `start`, `stop` | `group`, `resource_id`, `action`, `desired`, `at` | Performing a start/stop on a resource |
| Failure | `error` | `group`, `resource_id`, `error`, `at` | Recording a per-resource or per-group failure |
| Recovery | `recovered` | `group`, `resource_id`, `at` | A recorded `last_error` clearing |

For per-group failures and recoveries, `resource_id` becomes `group#<group name>`.

### Deduplication

A failure notification is emitted on the first occurrence and whenever the error text changes. While the same error persists, only the record on `status#` is updated.

On recovery, `recovered` is notified once and `last_error` is cleared. A recovery that comes with a successful action needs no separate notification, since the action notification covers it.

A per-group error is cleared exactly once, after all of that group's work is finished. Clearing it right after discovery and then recording another per-group error during the resource loop would repeat "clear and notify → record and notify" every cycle, flapping the notifications.

### Subject constraints

To fit the SNS Subject constraints (ASCII, 100 characters), non-ASCII characters in the subject are replaced with `?` and the result is truncated. The full resource ID is in the body.

### Notification failures

A Publish that fails after a successful action is logged only; the action is not turned into an error.
