# ECS service configuration

This document covers DynamoDB group settings, cheapskate-cli operations, and startup and scaling tags for an ECS service. Its cheapskate resource type name is ecs-service.

## Purpose of the settings

Startup count selects capacity when resuming from zero. Normal bounds define the allowed operating range. Bounds mode selects tags to continuously maintain that range or scheduled to delegate bounds changes after startup to AWS Scheduled Scaling. For scope and mode selection, see [Purpose of ECS scaling management](../ecs.md#purpose-and-scope).

## Eligibility

REPLICA services with long ARNs containing the cluster name are eligible. Tags on a cluster or task definition do not replace settings on the service itself.

For the CLI context and AWS connection used below, see [Shared prerequisites](README.md#shared-prerequisites).

## DynamoDB and CLI

Schedules and overrides follow [Shared DynamoDB settings](README.md#shared-dynamodb-settings). ECS services also require this group attribute:

| Attribute | DynamoDB type | Purpose and constraint |
| --- | --- | --- |
| ecs_max_count | Number | Maximum allowed cheapskate:desired-count and cheapskate:scaling-max for each service; 1–2147483647 |

ecs_max_count is required for startup and running configuration updates. Stopping does not apply this limit. A group used only for stopping can omit it, but invalid attribute types or values invalidate the group and prevent stopping too.

The limit applies to each service, not to the group's total count. It does not constrain current capacity or bounds set by Scheduled Scaling.

Create group dev using the shared CLI operations, then register its limit before setting the service's membership tag. To register a limit of 3:

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

For schedule and override registration and changes, see [Shared CLI operations](README.md#shared-cli-operations).

## Resource tags

In addition to the shared cheapskate:group tag, set these tags on the ECS service itself. All values are strings:

| Tag | Default | Meaning and constraint |
| --- | --- | --- |
| cheapskate:desired-count | 1 | Startup count from all-zero counts or 0/0 bounds; 1–2147483647 |
| cheapskate:scaling-min | cheapskate:desired-count | Normal MinCapacity; 0–2147483647 |
| cheapskate:scaling-max | cheapskate:desired-count | Normal MaxCapacity; 0–2147483647 |
| cheapskate:scaling-bounds-mode | scheduled | Bounds management: tags maintains tag bounds and suspends Scheduled Scaling; scheduled retains bounds changes after startup and enables Scheduled Scaling |

Defaults apply only to absent tags; empty strings are invalid. Tag counts must satisfy cheapskate:scaling-min <= cheapskate:desired-count <= cheapskate:scaling-max. Startup and running configuration updates also require the cheapskate:desired-count and cheapskate:scaling-max tag values to be no greater than the DynamoDB ecs_max_count attribute. All tags are validated whether or not a scalable target exists; invalid tags prevent stopping too.

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

cheapskate:desired-count is a startup count, not a continuously enforced running count. Without a scalable target, stopping sets desiredCount=0 and starting sets startup capacity only when all counts are zero. Cheapskate does not create scalable targets.

With a scalable target, stopping sets bounds to 0/0 and suspends Scheduled Scaling. Starting initializes capacity, then applies normal bounds and the suspension state corresponding to bounds mode. While running, tags maintains normal bounds. In scheduled mode with AWS suspension cleared, bounds and capacity changed by Scheduled Scaling are retained.

For the inspection command, see [Shared verification](README.md#shared-verification). In the show JSON output, check group.ecs_max_count and the target resource's config, counts and bounds and ScheduledScalingSuspended in live.detail, and live_error. Stopping completes only when ECS desiredCount, runningCount, and pendingCount are all zero.

For tag changes and partial failures, see [ECS startup, shutdown, and scaling](../ecs.md). For bounds and suspension left behind after management ends, see [Daily operations](../operations.md#ending-management).
