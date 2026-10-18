# ECS service configuration

This document covers DynamoDB group settings, cheapskate-cli operations, and startup and scaling tags for an ECS service. Its cheapskate resource type name is ecs-service.

## Purpose of the settings

Startup count selects capacity when resuming from zero. Normal bounds define the allowed operating range. Bounds mode selects tags to continuously maintain that range or scheduled to delegate bounds changes after startup to AWS Scheduled Scaling. For scope and mode selection, see [Purpose of ECS scaling management](../ecs.md#purpose-and-scope).

## Eligibility

REPLICA services with long ARNs containing the cluster name are eligible. Tags on a cluster or task definition do not replace settings on the service itself.

For the CLI context and AWS connection used below, see [CLI example prerequisites](../cli_records.md#prerequisites).

## DynamoDB and CLI

For schedule and override attribute formats, see [Group configuration DynamoDB format](../group_config.md#dynamodb-format). ECS service startup and running configuration updates require ecs_max_count to be registered for the group. For its type, range, and behavior, see [ECS capacity limit](../group_config.md#ecs-capacity-limit). Invalid attribute types or values invalidate the group and prevent stopping too.

Create group dev using the [CLI configuration examples](../cli_records.md), then register its limit before setting the service's membership tag. To register a limit of 3:

```sh
cheapskate-cli -context dev ecs-max-count --group dev 3
```

The ecs-max-count command registers or changes only the limit on an existing group, preserving its schedule and override. The saved attribute has this DynamoDB JSON representation:

```json
{"ecs_max_count": {"N": "3"}}
```

To change the limit to 4:

```sh
cheapskate-cli -context dev ecs-max-count --group dev 4
```

To remove the limit attribute:

```sh
cheapskate-cli -context dev clear-ecs-max-count --group dev
```

The clear-ecs-max-count command returns the limit to an unregistered state. It does not stop the current service. Subsequent startup and running configuration updates fail until a limit is registered again. Use a stopped override to maintain zero tasks.

For schedule and override registration and changes, see the [CLI configuration examples](../cli_records.md).

## Resource tags

In addition to the shared cheapskate:group tag, set these tags on the ECS service itself. All values are strings:

| Tag | Default | Meaning and constraint |
| --- | --- | --- |
| cheapskate:desired-count | 1 | Startup count from all-zero counts or 0/0 bounds; 1–2147483647 |
| cheapskate:scaling-min | cheapskate:desired-count | Normal MinCapacity; 0–2147483647 |
| cheapskate:scaling-max | cheapskate:desired-count | Normal MaxCapacity; 0–2147483647 |
| cheapskate:scaling-bounds-mode | scheduled | Bounds management mode |

The bounds management modes behave as follows:

| Value | Behavior |
| --- | --- |
| tags | Maintains tag bounds; suspends Scheduled Scaling |
| scheduled | Retains post-startup bounds changes; enables Scheduled Scaling |

Defaults apply only to absent tags; empty strings are invalid. Tag counts must satisfy cheapskate:scaling-min <= cheapskate:desired-count <= cheapskate:scaling-max. All tags are validated whether or not a scalable target exists; invalid tags prevent stopping too.

For service api in cluster dev, set a startup count of 2 and normal bounds of 1–3:

```sh
aws ecs tag-resource --region ap-northeast-1 \
  --resource-arn arn:aws:ecs:ap-northeast-1:123456789012:service/dev/api \
  --tags key=cheapskate:group,value=dev \
    key=cheapskate:desired-count,value=2 \
    key=cheapskate:scaling-min,value=1 \
    key=cheapskate:scaling-max,value=3 \
    key=cheapskate:scaling-bounds-mode,value=tags
```

ECS tag arguments use lowercase key and value. For arguments and long ARNs, see [AWS CLI tag-resource](https://docs.aws.amazon.com/cli/latest/reference/ecs/tag-resource.html).

## Applied behavior and verification

For startup, shutdown, tag changes, and partial failures, see [ECS startup, shutdown, and scaling](../ecs.md). For inspection commands, see [Daily operations](../operations.md#inspecting-configuration-and-current-state). Check these show JSON fields:

| Field | Check |
| --- | --- |
| group.ecs_max_count | Saved group capacity limit |
| resources[].config | Target service tag settings |
| resources[].live.detail | Counts, bounds, and ScheduledScalingSuspended |
| resources[].live_error | State inspection errors |

Stopping completes only when ECS desiredCount, runningCount, and pendingCount are all zero.

For bounds and suspension left behind after management ends, see [Daily operations](../operations.md#ending-management).
