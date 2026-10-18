# Troubleshooting

This document lists checks and remedies for unexpected configuration, resource operations, ECS scaling, and monitoring results.

## Initial checks

Use CLI show or the web console to inspect group configuration, resource membership tags, and current AWS state. Check live_error in the show JSON output for state inspection failures. Check reconciler summary error counts and the relevant group-error or resource-error logs. For inspection, see [Logs, metrics, and notifications](monitoring.md).

## Configuration errors

Common symptoms and remedies are:

| Symptom | Check or remedy |
| --- | --- |
| Group listed as invalid | Check unknown attributes, DynamoDB types, cron pairs, override values, and expiration |
| Timed override rejected | Register both cron fields and use a future expiration |
| Schedule change rejected after expiration | Clear the expired override or replace it |
| Override cannot be cleared | Add a schedule first; for override-only groups, replace the override or delete the group after ending management |
| HTTP 409 or CLI code 2 during a write | Read again, inspect configuration, and explicitly repeat the intended change |
| Web console returns HTTP 403 | Check forwarded Host, ALLOWED_HOSTS, and the form's Origin |

For values and constraints, see [Group configuration](group_config.md). Back up items that cannot be repaired through the CLI or web console before deliberately deleting and recreating them. Do not concurrently delete/recreate and modify the same group.

## Expected start or stop not applied

Compare group configuration and current AWS state:

| Check | Remedy |
| --- | --- |
| Active disabled override | Replace it with running/stopped or return to the schedule |
| Unexpected scheduled state | Check DEFAULT_TIMEZONE and the most recent start/stop occurrences |
| Resource absent from inspection | Check cheapskate:group and Resource Groups Tagging API discovery |
| Unregistered or invalid membership | Match the tag value to the DynamoDB group name |
| Resource inspection or operation error | Check IAM, AWS API errors, and throttling |
| RDS or EC2 transitioning | Wait for a later cycle and inspect the AWS transition |
| Unsupported resource configuration | Register a resource satisfying eligibility requirements |

For timing and eligibility, see [Start and stop behavior](resource_flow.md).

## Unexpected ECS counts or suspension

ECS configuration and current state are separate values. Check according to the symptom:

| Symptom | Check or remedy |
| --- | --- |
| Startup or repair fails | Check ecs_max_count, tag types, count/bounds constraints, and the group limit |
| Startup-count tag change does not change capacity | cheapskate:desired-count applies only from all-zero counts or 0/0 bounds |
| runningCount differs from desiredCount | Task transitions are asynchronous; inspect ECS events and later cycles |
| Resuming Scheduled Scaling changes count | Normal bounds may have adjusted an out-of-range desiredCount |
| Bounds differ from normal tags | cheapskate:scaling-bounds-mode=scheduled retains Scheduled Scaling changes; select tags to maintain tag bounds |
| Suspension cannot be cleared while stopped | A stopped request maintains ScheduledScalingSuspended=true |
| Service restarts after Scheduled Scaling sets zero | Use cheapskate's stop schedule or stopped override to remain at zero |

For values, see [Resource tags](resource_tag.md#ecs-configuration-tags) and [Group configuration](group_config.md#ecs-capacity-limit). For state progressions, see [ECS startup, shutdown, and scaling](ecs.md#configuration-and-current-state-examples).

## Globally aborted cycles

When Lambda returns an error without summary, check DynamoDB reads and resource discovery. Verify dynamodb:Query and tag:GetResources permissions and connectivity, plus rds:DescribeDBClusters when DB clusters are discovered. Check the ARN format and cluster resolution errors in the log. For inspection permissions including resource-ID queries, see [AWS resources and IAM](aws_resources.md#shared-resource-inspection). Individual resource changes do not begin in a cycle whose global reads fail.

## Missing notifications or metrics

Check NOTIFICATION_TOPIC_ARN, SNS permissions, and subscriptions. Notifications can fail after a successful operation. For missing or duplicate notifications and metric emission conditions, see [Logs, metrics, and notifications](monitoring.md).
