# Resource configuration and shared settings

This document covers DynamoDB settings, cheapskate-cli operations, and membership tags shared by all resource types. The documents in this directory describe settings specific to each type.

## Resource types

Choose the document for the resource being configured:

| Resource | cheapskate resource type name | Tag location | Specific settings |
| --- | --- | --- | --- |
| [RDS DB instance](rds-instance.md) | rds-instance | DB instance | None |
| [Aurora DB cluster](rds-cluster.md) | rds-cluster | DB cluster | None |
| [EC2 instance](ec2-instance.md) | ec2-instance | EC2 instance | Exclusion through the Auto Scaling group membership tag |
| [ECS service](ecs-service.md) | ecs-service | ECS service | Group capacity limit, startup count, normal bounds, and Scheduled Scaling suspension |

## Shared prerequisites

The examples use an existing cheapskate table, the ap-northeast-1 region, and the Asia/Tokyo time zone. Set the reconciler's DEFAULT_TIMEZONE to Asia/Tokyo as well. Register a CLI context named dev:

```sh
cheapskate-cli config set-context dev \
  --table cheapskate --region ap-northeast-1 --timezone Asia/Tokyo
```

-context dev selects a connection; --group dev selects a group in DynamoDB. Configure AWS credentials first. If the context specifies --profile, supply the same --profile to AWS CLI tagging commands. Replace example ARNs, account IDs, and resource names with actual values.

For infrastructure preparation, see [Setup](../setup.md).

## Shared DynamoDB settings

DynamoDB stores one item per group, with no separate items for individual resources. Changing a group affects all its members, regardless of resource type. Use separate groups when resources need different schedules or overrides.

All resource types use these attributes:

| Attribute | DynamoDB type | Purpose |
| --- | --- | --- |
| pk | String | Fixed value CONFIG |
| sk | String | GROUP#NAME, where NAME is the membership tag value |
| start_cron, stop_cron | String | Startup and shutdown times; specify both as five-field cron expressions |
| override | String | running maintains running state; stopped maintains stopped state; disabled suppresses reconciler inspection and changes |
| override_expires_at | Number | Override expiration in Unix seconds; omit for an indefinite override |

A schedule or an override is required. Timed overrides also require a schedule. An active override takes precedence; clearing it or letting it expire returns control to the schedule at that time.

Only ECS services use ecs_max_count. RDS, Aurora, and EC2 do not require it. However, an invalid attribute type or value makes the entire group invalid, preventing processing of its other resources as well. Omit unused attributes rather than storing 0 or NULL.

For group name, cron, override expiration, and DynamoDB attribute constraints, see [Group configuration](../group_config.md). For ECS capacity limits, see [ECS DynamoDB and CLI settings](ecs-service.md#dynamodb-and-cli).

## Shared CLI operations

Register the group before setting resource membership tags. For ECS, also register the capacity limit before tagging the service. To register a weekday 09:00–18:00 schedule:

```sh
cheapskate-cli -context dev schedule --group dev \
  -start '0 9 * * 1-5' -stop '0 18 * * 1-5'
```

A newly created group has this DynamoDB JSON representation:

```json
{
  "pk": {"S": "CONFIG"},
  "sk": {"S": "GROUP#dev"},
  "start_cron": {"S": "0 9 * * 1-5"},
  "stop_cron": {"S": "0 18 * * 1-5"}
}
```

For an existing group, schedule updates only the cron attributes and preserves the override and ECS limit. If no schedule is needed, create the group with an indefinite override. After registering a schedule, the following are independent operations:

| Purpose | Command | Saved changes |
| --- | --- | --- |
| Keep running for two hours | cheapskate-cli -context dev override --group dev running -for 2h | override=running and override_expires_at two hours after execution |
| Keep stopped indefinitely | cheapskate-cli -context dev override --group dev stopped | override=stopped; expiration removed |
| Suspend automatic control | cheapskate-cli -context dev override --group dev disabled | override=disabled; expiration removed |
| Clear the override | cheapskate-cli -context dev clear-override --group dev | Remove override and override_expires_at |

Omitting -for also makes a running override indefinite. Clear-override requires a schedule. Expired override_expires_at attributes remain stored; clear or replace the override before another configuration update.

All DynamoDB group settings can be registered through cheapskate-cli. For ecs_max_count operations, see [ECS DynamoDB and CLI settings](ecs-service.md#dynamodb-and-cli). For all arguments and exit codes, see [CLI reference](../cli.md).

## Shared membership tag

Set the following tag on the AWS resource itself using AWS CLI, the AWS console, or IaC:

| Tag | Value | Default |
| --- | --- | --- |
| cheapskate:group | A registered DynamoDB group name; dev in these examples | None |

Resources without the tag are not discovered. An empty value, invalid group name, or unregistered name is an error for that resource. Each resource document provides its tagging command and target ARN.

For discovery and value constraints, see [Resource tags](../resource_tag.md).

## Shared verification

cheapskate-cli saves group configuration. Startup or shutdown begins in a later reconciler cycle that reads it. CLI success does not mean that AWS operations have completed. Inspect configuration and current state with:

```sh
cheapskate-cli -context dev -output json show --group dev
```

In the show JSON output, inspect group for saved settings and each resources entry's ref and type to identify the resource. Each entry's live.state and live.detail contain current AWS state; live_error reports state inspection failures. Show can return exit code 0 with individual resource failures, so check live_error too.

Manual changes to managed resources are reconciled back to the group's request. Disabled overrides, membership-tag removal, and group deletion leave current AWS settings in place. For timing and failures, see [Start and stop behavior](../resource_flow.md). For ending management, see [Daily operations](../operations.md#ending-management).
