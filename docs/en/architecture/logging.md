# Logging

## Kinds of log

The format and purpose of each component's log are given below.

| Component | Format | Destination | Purpose |
|---|---|---|---|
| reconciler | `log/slog` JSON handler | stderr | Execution log |
| Web console | `log/slog` JSON handler | stderr | Startup log, request log, error log |
| `dev-bootstrap` | the standard `log` | stderr | For `make dev` only; not in the images |

## JSON logs

Both the reconciler and the web console write one JSON record per line to stderr through the `log/slog` JSON handler.

### An example record

The record the reconciler writes when it performs one action is given below.

```json
{
  "time": "2026-07-15T03:00:01Z",
  "level": "INFO",
  "msg": "action",
  "group": "dev",
  "resource_id": "rds-instance#dev-db",
  "action": "stop",
  "desired": "stopped"
}
```

### Attributes

A ✓ in the Required column means the attribute appears on every record. Whether a non-✓ attribute appears is decided per log kind, and the event list for each component states which.

An attribute of the same name carries the same meaning across components. All attributes are given below.

| Attribute | Required | Type | Meaning | Format / range |
|---|---|---|---|---|
| `time` | ✓ | string | When the record was written | RFC3339 (the `log/slog` default) |
| `level` | ✓ | string | Severity | `INFO`, `WARN`, `ERROR` |
| `msg` | ✓ | string | The kind of log | A value from the component's event list |
| `group` | - | string | Group name | A string valid as a group name |
| `resource_id` | - | string | ID of the resource concerned | `<resource type>#<name>`. For per-group failures, `group#<name>` |
| `desired` | - | string | The state the schedule calls for | `running`, `stopped` |
| `detail` | - | string | Type-specific detail of the observed state | Free text (for example `desiredCount=2`) |
| `since` | - | string | When the resource was first taken to be transitioning | RFC3339 (UTC) |
| `source` | - | string | Origin of the EventBridge event that triggered the run | The event's `source` (for example `aws.rds`) |
| `reason` | - | string | Why it was treated that way | Free text |
| `action` | - | string | The operation performed or requested | A value from the component's action list |
| `result` | - | string | The outcome of the operation | Free text (the same wording shown on screen) |
| `reconciled` | - | number | Resources seen in one cycle | A non-negative integer |
| `actions` | - | number | Actions performed in one cycle | A non-negative integer |
| `errors` | - | number | Failures in one cycle | A non-negative integer |
| `pruned` | - | number | Records the prune managed to delete | A non-negative integer. Present only when some deletions failed |
| `status` | - | number | The HTTP status returned | An HTTP status code |
| `duration_ms` | - | number | Time from receiving the request to finishing it | Milliseconds (integer) |
| `table` | - | string | The DynamoDB table holding the state | Table name |
| `base_path` | - | string | The path prefix as the browser sees it | The value of `BASE_PATH`; empty if unset |
| `timezone` | - | string | The default timezone | IANA timezone name (for example `Asia/Tokyo`) |
| `addr` | - | string | The listen address | `host:port`. Present both on Lambda and locally |
| `method` | - | string | HTTP method | `GET`, `POST` |
| `path` | - | string | Request path | The path without `BASE_PATH` |
| `query` | - | string | Query string | The raw string; empty if there is none |
| `client` | - | string | IP the request came from | An IP address (no port). On Lambda this is `sourceIp` from the event's `requestContext` — the IP the resource policy used to allow the request — and `X-Forwarded-For` is not consulted |
| `error` | - | string | What failed | The string from `err.Error()`, including any wrapped cause |
| `_aws` | - | object | EMF metadata ([metrics.md](metrics.md)) | The same record carries a number per metric name |

> [!NOTE]
> Only when initialization inside the Lambda function fails does `msg` carry an arbitrary error message that is not in the lists below.

## reconciler

### Event list

The events the reconciler writes are given below.

| `msg` | level | Attributes | Meaning |
|---|---|---|---|
| `event-received` | INFO | `source`, `reason` | Started from an EventBridge event |
| `skip-lease-held` | INFO | — | Returned successfully because another invocation owns the reconcile lease |
| `lease-release-failed` | ERROR | `owner`, `error` | Failed to release the reconcile lease owned by this invocation |
| `orphaned-group-data` | WARN | `group` | Inconsistent DynamoDB data for a group |
| `action` | INFO | `group`, `resource_id`, `operation_id`, `action`, `desired` | A resource was started or stopped |
| `action-recovered` | INFO | `group`, `resource_id`, `operation_id`, `action`, `desired` | Completed a pending record from the observed AWS state |
| `skip-pending-transition` | INFO | `resource_id`, `operation_id`, `detail`, `since` | Deferred a pending operation while its target is transitioning |
| `skip-pending-action` | INFO | `resource_id`, `operation_id`, `observed`, `age` | Deferred while waiting for a pending action to take effect |
| `skip-transitioning` | INFO | `resource_id`, `detail`, `since` | Skipped, the resource being taken as mid-transition |
| `skip-not-found` | INFO | `resource_id` | Skipped, the resource not existing |
| `error` | ERROR | `group`, `resource_id`, `error` | A per-resource or per-group failure |
| `summary` | INFO | `reconciled`, `actions`, `errors` | Summary of one cycle |
| `metrics` | INFO | `_aws` + metric names | EMF metrics ([metrics.md](metrics.md)) |
| `metrics-disabled` | INFO | `reason` | Custom metrics are disabled |
| `action-notify-failed` | ERROR | `group`, `resource_id`, `operation_id`, `attempt`, `max_attempts`, `error` | Publishing an action notification failed |
| `action-notify-abandoned` | ERROR | `group`, `resource_id`, `operation_id`, `attempts` | An action notification was abandoned after the maximum attempts |
| `pending-operation-abandon-failed` | ERROR | `resource_id`, `operation_id`, `error` | Clearing pending after a failed AWS action also failed |
| `recovery-notify-failed` / `error-notify-failed` | ERROR | `group`, `resource_id`, `attempt`, `max_attempts`, `error` | Publishing a recovery or error notification failed |
| `recovery-notify-abandoned` / `error-notify-abandoned` | ERROR | `group`, `resource_id`, `attempts` | A recovery or error notification was abandoned after the maximum attempts |
| `error-clear-failed` | ERROR | `group`, `resource_id`, `error` | Clearing `last_error` after a recovery failed |
| `error-record-failed` | ERROR | `group`, `resource_id`, `error` | Writing the failure to `last_error` failed |
| `transitioning-mark-failed` | ERROR | `resource_id`, `error` | Writing `transitioning_since` failed |
| `transitioning-clear-failed` | ERROR | `resource_id`, `error` | Clearing `transitioning_since` failed |

### Action list

The values the `action` attribute takes are given below.

| `action` | Meaning |
|---|---|
| `start` | A stopped resource was started |
| `stop` | A running resource was stopped |

### Errors not recorded in `last_error`

Ordinary resource errors go to the log, `last_error` in status, and SNS. The following are failures of the recording path or auxiliary state and are not written to `last_error`.

| Event | Why the log only |
|---|---|
| `*-notify-failed`, `*-notify-abandoned` | Notification delivery is independent of AWS action and reconcile success |
| `error-record-failed`, `pending-operation-abandon-failed` | Inability to record in DynamoDB cannot be recorded through the same path |
| `lease-release-failed` | The TTL expires the lease, independently of resource action success |
| `transitioning-*-failed` | The information is for auditing and is not treated as a reconciler failure |

## Web console

### Event list

The events the web console writes are given below.

| `msg` | level | Attributes | Meaning |
|---|---|---|---|
| `startup` | INFO | `table`, `base_path`, `timezone`, `addr` | Startup (one line per cold start) |
| `request-start` | INFO | `method`, `path`, `query`, `client` | A request was received |
| `request-end` | INFO | the `request-start` attributes + `status`, `duration_ms` | The request finished |
| `request-failed` | ERROR | the `request-start` attributes + `status`, `error` | A 4xx/5xx was returned (the same wording appears on screen) |
| `operation` | INFO | `action`, `group`, `client`, `result` | An operation that rewrites configuration succeeded |
| `operation-failed` | ERROR | `action`, `group`, `client`, `error`, `pruned` | The same failed (the screen is reached by a redirect with `?err=`) |

> [!NOTE]
> When `action` is `doctor-prune`, no `group` is present. `pruned` appears only when the prune itself ran but some individual deletions failed.

If startup fails, one ERROR line outside the events above is written and the process exits with code 1.

### Action list

The values the `action` attribute takes are given below.

| `action` | Meaning |
|---|---|
| `set-selector` | The group's selector was saved (creating the group if absent) |
| `schedule` | The group's schedule was saved |
| `pin` | The state was fixed, ignoring the schedule |
| `unpin` | The fixed state was released |
| `override` | A state taking precedence over the schedule was set with an expiry |
| `clear-override` | The time-limited precedence was removed |
| `disable` | The group was excluded from convergence |
| `remove-group` | The group was deleted |
| `doctor-prune` | Orphaned records were deleted |
