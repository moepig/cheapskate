# Daily operations

This document provides procedures for checking configuration and current state, changing schedules, temporarily starting or stopping resources, changing ECS settings, and ending management.

## Inspecting configuration and current state

Set the CLI table and time zone, then inspect configuration and resources:

```sh
export CHEAPSKATE_TABLE=cheapskate
export DEFAULT_TIMEZONE=Asia/Tokyo
cheapskate-cli list
cheapskate-cli -output json show --group dev
```

List displays stored configuration. Show displays member configuration tags and current AWS state without changing resources or waiting for transitions. A resource with live_error failed inspection.

For browser operations, see [Web console](web_console.md). For arguments and output formats, see [CLI reference](cli.md).

## Registering or changing a schedule

For weekday operation from 09:00 to 18:00, use:

```console
cheapskate-cli schedule --group dev -start '0 9 * * 1-5' -stop '0 18 * * 1-5'
```

This creates an absent group. Existing overrides remain and take precedence while active. Clear or replace an expired override before changing the schedule.

## Temporary changes with overrides

An override changes the running/stopped request or pauses control while retaining the schedule.

### Extending a running period

To keep a scheduled dev group running for two hours:

```console
cheapskate-cli override --group dev running -for 2h
```

At expiration, the schedule's state at that time takes over. Timed overrides require a schedule.

### Keeping resources stopped

To request an indefinite stop:

```console
cheapskate-cli override --group dev stopped
```

### Pausing automatic control

To allow manual resource changes without automatic control:

```console
cheapskate-cli override --group dev disabled
```

Disabled does not itself change AWS resources; it suppresses subsequent inspection and changes.

### Returning to the schedule

To clear the override:

```console
cheapskate-cli clear-override --group dev
```

An override-only group cannot clear its override. Add a schedule first or replace the override. For stored-item examples, see [CLI group configuration examples](cli_records.md).

## ECS configuration changes

ECS settings are split between a group limit in DynamoDB and tags on the service. For meanings and application timing, see [ECS startup, shutdown, and scaling](ecs.md).

### Registering an ECS capacity limit

Before allowing startup, register a Number ecs_max_count on the group item. To set a limit of 4 on an existing dev group:

```sh
aws dynamodb update-item \
  --table-name cheapskate \
  --key '{"pk":{"S":"CONFIG"},"sk":{"S":"GROUP#dev"}}' \
  --update-expression 'SET ecs_max_count = :maximum' \
  --expression-attribute-values '{":maximum":{"N":"4"}}' \
  --condition-expression 'attribute_exists(pk)'
```

CLI and web-console schedule and override operations retain this value. For constraints, see [Group configuration](group_config.md#ecs-capacity-limit).

### Suspending and resuming Scheduled Scaling

To suspend, set the service's cheapskate/scheduled-scaling-paused tag to true and confirm ScheduledScalingSuspended=true after a successful cycle.

Before resuming, confirm that cheapskate/scaling-min and cheapskate/scaling-max specify the intended normal range. Resuming applies those bounds, and AWS adjusts capacity outside the range. To resume:

1. Set cheapskate/scheduled-scaling-paused to false or remove it.
2. Set the group's request to running.
3. After a successful cycle, inspect show or the web console for ScheduledScalingSuspended=false and normal bounds.

A stopped request keeps AWS suspension enabled even after clearing the tag. Scheduled actions whose execution time passed during suspension are not replayed.

## Ending management

First choose the running/stopped state and ECS Scheduled Scaling state to retain. For a running ECS service, set the intended normal bounds in tags and request false suspension to enable Scheduled Scaling, or true to keep it suspended. A stopped service retains bounds 0/0 and suspension.

To end management:

1. Set an indefinite running or stopped override.
2. Wait at least one configured reconciler Lambda timeout after the write completes.
3. Invoke the reconciler manually or wait for the next periodic invocation.
4. Use show to confirm every resource is stable in the selected state. For ECS, also inspect bounds and ScheduledScalingSuspended in live.Detail.
5. Remove cheapskate:group from the resources.
6. Delete the group with cheapskate-cli remove --group <group-name>.

Do not change the group during this sequence. Waiting allows invocations holding older configuration to finish before confirming the final state. Deleting a group does not itself change AWS resources.

For Terraform-managed groups, change and delete configuration through Terraform. See [Terraform group configuration](terraform_records.md).
