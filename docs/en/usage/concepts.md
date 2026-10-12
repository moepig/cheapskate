# Groups and schedules

cheapskate groups AWS resources and periodically maintains the running or stopped state selected by a schedule or override. This document explains membership, requested states, and how time determines the request.

## Groups and resource membership

A group is a set of resources with shared running and stopped settings. Group configuration is stored in DynamoDB. Each resource's cheapskate:group tag names its group. A group can contain multiple supported resource types.

Configuration and current state have separate sources:

| Information | Location | Meaning |
| --- | --- | --- |
| Group configuration | DynamoDB | Normal start and stop times, override, and ECS capacity limit |
| Resource configuration | AWS resource tags | Membership and ECS startup count, normal bounds, and suspension request |
| Current state | AWS services | Current running/stopped state or ECS task counts and scaling settings |

Saving configuration through the CLI or web console does not wait for AWS resources to start or stop. The reconciler is the program that periodically compares configuration with current state and applies necessary changes.

## Schedules

A schedule is a pair of five-field cron expressions: start_cron for starting and stop_cron for stopping. Both are interpreted in the DEFAULT_TIMEZONE runtime setting.

Each cycle selects the state of the more recent start or stop occurrence. A tie selects stopped. Creating a group during its running period selects running in the next cycle; it does not wait for the next start occurrence.

For weekday operation from 09:00 to 18:00, use:

```text
start_cron: 0 9 * * 1-5
stop_cron: 0 18 * * 1-5
DEFAULT_TIMEZONE: Asia/Tokyo
```

## Overrides

An override takes precedence over the schedule. It can be indefinite or expire at a specified time. Available states are:

| Override | Behavior |
| --- | --- |
| running | Keep member resources running |
| stopped | Keep member resources stopped |
| disabled | Skip state inspection and changes for member resources |

Without an active override, the current schedule applies. Clearing or expiring an override returns to the schedule's state at that time. Setting disabled does not itself change any AWS resource.

### Extending a running period

For a weekday 09:00–18:00 group, a running override set Monday at 17:00 and expiring at 20:00 produces the following requests. Times use Asia/Tokyo.

| Time | Scheduled state | Override | Applied request |
| --- | --- | --- | --- |
| Monday 09:00 | Running | Absent | running |
| Monday 17:00 | Running | Active running | running |
| Monday 18:00 | Stopped | Active running | running |
| Monday 20:00 | Stopped | Expired | stopped |
| Tuesday 09:00 | Running | Expired | running |

A cycle after 20:00 selects stopped based on the 18:00 stop occurrence. Resource operations begin in the cycle that reads that request.

## Configuration and operation references

For names, cron constraints, and expiration values, see [Group configuration](group_config.md). For commands, see [Daily operations](operations.md). For timing and supported resources, see [Start and stop behavior](resource_flow.md).
