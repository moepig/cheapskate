# CLI group configuration examples

This document shows CLI commands and saved DynamoDB configuration for weekday operation, indefinite overrides, and timed overrides.

## Prerequisites

These examples use an existing cheapskate DynamoDB table and a reconciler running every five minutes. Both the CLI and reconciler use DEFAULT_TIMEZONE=Asia/Tokyo. For infrastructure preparation, see [Setup](setup.md).

Configure AWS credentials, then set up and select a CLI context:

```sh
export AWS_REGION=ap-northeast-1
export CHEAPSKATE_TABLE=cheapskate
export DEFAULT_TIMEZONE=Asia/Tokyo
cheapskate-cli config set-context dev \
  --table cheapskate --region ap-northeast-1 --timezone Asia/Tokyo
cheapskate-cli config use-context dev
```

The remaining commands use the selected dev context. In resource-tagging examples, -context dev selects the CLI connection, while --group dev selects the DynamoDB group.

If the context specifies --profile, supply the same --profile to AWS CLI tagging commands.

Replace example ARNs, account IDs, and resource names with actual values.

DynamoDB stores one item per group, with pk=CONFIG and sk=GROUP#<group-name>.

The CLI saves group configuration without creating, tagging, starting, or stopping AWS resources.

Register the group before setting cheapskate:group=<group-name> on member resources. For ECS, also register the capacity limit before tagging the service. The reconciler later reads the configuration and tags to apply the current request.

For ECS, register the group's DynamoDB ecs_max_count attribute with cheapskate-cli ecs-max-count --group NAME COUNT before allowing startup. The cheapskate:desired-count and cheapskate:scaling-max tag values must be within this Number limit. See [Registering an ECS capacity limit](operations.md#registering-an-ecs-capacity-limit).

## Weekday operation

To create a dev group running weekdays from 09:00 to 18:00:

```sh
cheapskate-cli schedule --group dev \
  -start '0 9 * * 1-5' -stop '0 18 * * 1-5'
```

The resulting DynamoDB JSON item is below. S denotes a String attribute.

```json
{
  "pk": {"S": "CONFIG"},
  "sk": {"S": "GROUP#dev"},
  "start_cron": {"S": "0 9 * * 1-5"},
  "stop_cron": {"S": "0 18 * * 1-5"}
}
```

Schedule creates one dev item. For an existing group, it updates only the two cron attributes and preserves the override and ECS limit.

Resources tagged cheapskate:group=dev receive running requests from weekday 09:00 and stopped requests from 18:00. Friday's stopped state continues until Monday morning.

Registration at weekday 10:00 selects running in the next cycle rather than waiting for another cron occurrence.

## Indefinite overrides

To register three independent groups with different overrides:

```sh
cheapskate-cli override --group always-on running
cheapskate-cli override --group always-off stopped
cheapskate-cli override --group unmanaged disabled
```

All items use pk=CONFIG. Stored settings and effects are:

| sk | override String | Member behavior |
| --- | --- | --- |
| GROUP#always-on | running | Keep resources running |
| GROUP#always-off | stopped | Keep resources stopped |
| GROUP#unmanaged | disabled | Skip inspection and changes |

An indefinite override omits override_expires_at. Omitting -for also makes a running override indefinite.

Applied to an existing group, it preserves the schedule and ECS limit and removes any old expiration. Disabled itself does not change AWS state.

## Timed overrides

Timed overrides require a schedule, including stopped and disabled overrides.

For a dev group with a schedule, this example sets a running override at 17:00 on 2026-09-14 that expires at 20:00. The duration is measured from actual command execution:

```sh
cheapskate-cli override --group dev running -for 3h
```

Execution at 17:00:00 produces this item. N denotes a Number; expiration is stored in Unix seconds.

```json
{
  "pk": {"S": "CONFIG"},
  "sk": {"S": "GROUP#dev"},
  "start_cron": {"S": "0 9 * * 1-5"},
  "stop_cron": {"S": "0 18 * * 1-5"},
  "override": {"S": "running"},
  "override_expires_at": {"N": "1789383600"}
}
```

This update adds override and override_expires_at to the same dev item without increasing the item count.

The override remains active beyond the 18:00 stop time. A cycle at or after 20:00 ignores it and selects stopped from the schedule.

Expiration leaves attributes in DynamoDB; TTL does not delete them. Clear or replace an expired override before another configuration update. These operations preserve the ECS limit.

## Clearing an override and inspecting results

Clearing an override requires a schedule. To return dev to its schedule and inspect resources:

```sh
cheapskate-cli clear-override --group dev
cheapskate-cli -output json show --group dev
```

Clear-override removes override and override_expires_at while preserving cron attributes and the ECS limit. Subsequent cycles that read the configuration follow the schedule.

Show reads configuration and current AWS state without changing resources or waiting for transitions.

> [!NOTE]
> An override-only group cannot clear its override. Add a schedule or replace the override first. For group removal, follow [Ending management](operations.md#ending-management).

## Verifying configuration effects

CLI success means configuration was saved. Inspect show for actual completion of AWS transitions. For timing and supported resources, see [Start and stop behavior](resource_flow.md). For ECS counts and scaling examples, see [ECS configuration and current-state examples](ecs_examples.md#configuration-and-current-state-examples).
