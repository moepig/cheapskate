# Troubleshooting

This document lists checks and remedies for unexpected configuration, resource operations, ECS scaling, and monitoring results.

## Initial checks

Use CLI show or the web console to inspect group configuration, resource membership tags, and current AWS state. Check live_error in the show JSON output for state inspection failures.

Check reconciler summary error counts and the relevant group-error or resource-error logs. For inspection, see [Logs, metrics, and notifications](monitoring.md).

## Configuration errors

Common symptoms and remedies are:

| Symptom | Check or remedy |
| --- | --- |
| Group listed as invalid | Check attribute constraints below |
| Timed override rejected | Register both cron fields and use a future expiration |
| Schedule change rejected after expiration | Clear the expired override or replace it |
| Override cannot be cleared | Add a schedule first; see alternatives below |
| HTTP 409 or CLI code 2 during a write | Reload and inspect before explicitly retrying |
| Web console returns HTTP 403 | Check forwarded Host, ALLOWED_HOSTS, and the form's Origin |

For an invalid group, check unknown attributes, DynamoDB types, cron pairs, override values, and expiration.

For an override-only group, replace the override or delete the group after ending management.

For values and constraints, see [Group configuration](group_config.md). Do not concurrently delete/recreate and modify the same group.

Back up items that cannot be repaired through the CLI or web console before deliberately deleting and recreating them.

## Expected start or stop not applied

Compare group configuration and current AWS state:

| Check | Remedy |
| --- | --- |
| Active disabled override | Replace it with running/stopped or return to the schedule |
| Unexpected scheduled state | Check time zone and start/stop occurrences below |
| Resource absent from inspection | Check membership tags and discovery below |
| Unregistered or invalid membership | Match the tag value to the DynamoDB group name |
| Resource inspection or operation error | Check IAM, AWS API errors, and throttling |
| RDS or EC2 transitioning | Wait for a later cycle and inspect the AWS transition |
| Unsupported resource configuration | Register a resource satisfying eligibility requirements |

For an unexpected scheduled state, check DEFAULT_TIMEZONE and the most recent start/stop occurrences. For an absent resource, check cheapskate:group and Resource Groups Tagging API discovery.

For timing and eligibility, see [Start and stop behavior](resource_flow.md).

## Unexpected ECS counts or suspension

ECS configuration and current state are separate values. Check according to the symptom:

| Symptom | Check or remedy |
| --- | --- |
| Startup or repair fails | Check limit and tag constraints below |
| Startup-count tag change does not change capacity | Check initialization conditions below |
| runningCount differs from desiredCount | Inspect ECS events and later cycles |
| Resuming Scheduled Scaling changes count | Normal bounds may have adjusted an out-of-range desiredCount |
| Bounds differ from normal tags | Inspect bounds mode; see its effects below |
| Suspension cannot be cleared while stopped | A stopped request maintains ScheduledScalingSuspended=true |
| Service restarts after Scheduled Scaling sets zero | Use a stop schedule or stopped override to remain at zero |

Task startup and termination are asynchronous, so runningCount may differ from desiredCount. Inspect ECS events and later cycles.

For startup or repair failures, check ecs_max_count, tag types, count/bounds constraints, and whether tag values exceed the group limit.

The startup-count tag cheapskate:desired-count applies only under either condition:

- desiredCount, runningCount, and pendingCount are all zero.
- Scaling bounds are 0/0.

The cheapskate:scaling-bounds-mode=scheduled setting retains bounds changed by Scheduled Scaling. Select tags to maintain tag bounds.

For values, see [Resource tags](resource_tag.md#ecs-configuration-tags) and [Group configuration](group_config.md#ecs-capacity-limit). For state progressions, see [ECS configuration and current-state examples](ecs_examples.md#configuration-and-current-state-examples).

## Globally aborted cycles

When Lambda returns an error without summary, check DynamoDB reads and resource discovery. Individual resource changes do not begin in a cycle whose global reads fail.

Verify permissions and connectivity for:

- dynamodb:Query
- tag:GetResources
- rds:DescribeDBClusters when DB clusters are discovered

Check the ARN format and cluster resolution errors in the log. For inspection permissions including resource-ID queries, see [Shared IAM inspection permissions](iam.md#shared-resource-inspection).

## Missing notifications or metrics

Check NOTIFICATION_TOPIC_ARN, SNS permissions, and subscriptions. Notifications can fail after a successful operation. For missing or duplicate notifications and metric emission conditions, see [Logs, metrics, and notifications](monitoring.md).
