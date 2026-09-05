# Troubleshooting

## Start with the cycle summary

Each reconciler cycle writes structured logs and finishes with a `summary` record. Check `ReconciledResources`, successful actions, and group or resource error counts. When custom metrics are enabled, `ReconcileErrors` and `ReconcileAborted` provide alarm inputs, but logs contain the affected group, ARN, and cause.

A configuration Query failure or global resource-discovery failure aborts the cycle before any resource Describe or change call. Fix DynamoDB or `tag:GetResources` access and invoke again.

## Common configuration errors

| Symptom | Check |
| --- | --- |
| Group appears in the invalid-row output | Unknown attributes, attribute types, cron field count and reachability, paired cron attributes, override value, and expiry integer/range |
| Timed override is rejected | The group must already have both cron attributes, and the expiry must be in the future |
| Clearing an override is rejected | Add a schedule first, or remove an override-only group |
| HTTP 409 or CLI exit 2 during a write | Another request deleted or changed the required item state; reread and explicitly retry the intended operation |

Do not edit malformed items in place through an unvalidated path. If the CLI and web console cannot repair the item because full validation fails, back up the item, delete it deliberately, and recreate the group through a supported interface.

## Resource errors

The reconciler isolates these errors and continues with other resources:

- empty, invalid, or unconfigured `cheapskate:group` values;
- unsupported RDS engine or an RDS instance that belongs to a cluster;
- ECS services that are not `REPLICA` or have invalid restoration tags;
- missing adapter permissions or throttled service calls.

Transitional resources are intentionally skipped. A later five-minute cycle observes them again. Start and Stop delivery is at least once, so adapters and operator procedures must tolerate repeated absolute-state requests.

## No action occurs

Confirm that the group has an active `running` or `stopped` override, or a complete schedule. An active `disabled` override suppresses Describe and change calls for the group. For schedules, confirm `DEFAULT_TIMEZONE` and compare the most recent start and stop ticks.

Verify the fixed membership tag on the resource and `tag:GetResources` output. Discovery requests contain only the tag key, so the returned tag value must exactly match the configured group name.

## ECS does not restore correctly

Inspect the service tags before invoking Start. Present values, together with their defaults, must satisfy `0 <= min <= desired <= max`, and desired must be positive. The same validation applies without a scalable target. Tag validation happens before a modifying call, so a tag error leaves the service unchanged.

## Notifications and metrics

Notifications are sent only after a successful Start or Stop. A failed publish is retried once in the same invocation; a second failure is logged and does not undo the resource action. No recovery or error notification is sent.

Custom metrics require `METRICS_ENABLED=true`. cheapskate emits Embedded Metric Format records but does not create alarms or dashboards.
