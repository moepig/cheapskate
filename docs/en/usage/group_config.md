# Group configuration

This document defines group names, schedules, overrides, ECS capacity limits, and the data format for direct DynamoDB writes.

## Group names

A name contains 1–64 ASCII letters, digits, periods, underscores, or hyphens. It must begin with an ASCII letter or digit. Use the same name in each resource's cheapskate:group tag.

## Schedules and overrides

Every group requires a schedule, an override, or both. Supported combinations are:

| Configuration | Required fields |
| --- | --- |
| Schedule only | start_cron and stop_cron |
| Indefinite override only | override |
| Schedule and indefinite override | start_cron, stop_cron, override |
| Schedule and timed override | start_cron, stop_cron, override, override_expires_at |

Both cron expressions use five fields and must have reachable past and future occurrences. Searches are limited to 400 years in each direction. Time zones are not stored per group; the DEFAULT_TIMEZONE runtime setting applies.

When an offset change repeats a local time, each occurrence is a candidate. Nonexistent local times do not fire. Start and stop occurrences are compared as absolute instants.

override accepts running, stopped, or disabled. override_expires_at is an integer Unix timestamp in seconds, from 1 through 253402300799. New or updated configurations require an expiration in the future. For an indefinite override, omit the attribute; do not store zero or NULL.

A stored expiration at or before current time makes the override inactive. Attributes are not automatically deleted. Before changing configuration through the CLI or web console, clear an expired override or replace it with a new override.

## ECS capacity limit

The DynamoDB attribute ecs_max_count validates the startup-count tag cheapskate/desired-count and normal-maximum tag cheapskate/scaling-max for each member ECS service. Register a positive int32 Number from 1 through 2147483647. Startup and running configuration updates require this value. Stopping does not apply this limit.

The limit applies per service, not to the group's total task count. Scheduled Scaling bounds and current capacity do not pass through this validation.

Use the CLI ecs-max-count command to register or change the limit and clear-ecs-max-count to remove the attribute. Both require an existing group and preserve its schedule and override. The web console displays the value and retains it during schedule and override changes. For registration, changes, and removal, see [ECS service configuration](resources/ecs-service.md#dynamodb-and-cli).

## DynamoDB format

The table uses String partition key pk and String sort key sk. Store one item per group. Do not configure TTL or secondary indexes. Attributes are:

| Attribute | DynamoDB type | Value |
| --- | --- | --- |
| pk | String | CONFIG |
| sk | String | GROUP#<group-name> |
| start_cron | String | Start cron |
| stop_cron | String | Stop cron |
| override | String | running, stopped, or disabled |
| override_expires_at | Number | Override expiration in Unix seconds |
| ecs_max_count | Number | Per-service ECS capacity limit |

Unknown attributes, incorrect types, and invalid field combinations are configuration errors for that group. Do not add resource state or operation history to the item.

A dev group with a weekday 09:00–18:00 schedule and ECS limit of 3 has this DynamoDB JSON representation:

```json
{
  "pk": {"S": "CONFIG"},
  "sk": {"S": "GROUP#dev"},
  "start_cron": {"S": "0 9 * * 1-5"},
  "stop_cron": {"S": "0 18 * * 1-5"},
  "ecs_max_count": {"N": "3"}
}
```

Direct writes bypass CLI and web-console validation, so preserve attribute types and valid combinations. For examples and change constraints, see [Terraform group configuration](terraform_records.md).
