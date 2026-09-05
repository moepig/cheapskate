# Concepts

cheapskate periodically converges tagged AWS resources to the state selected for their group. Configuration consists only of a schedule and an optional override.

## Groups and membership

A resource belongs to the group named by its `cheapskate:group` tag. An empty tag, an invalid group name, or the name of an unconfigured group is a resource error. A group name contains 1–64 ASCII letters, digits, periods, underscores, or hyphens.

The reconciler loads every group and every tagged resource before it makes any Describe, Start, or Stop call. A global read failure aborts the cycle. An invalid group or resource is isolated so other resources continue.

## Desired-state resolution

An active override takes precedence:

1. `disabled` omits the group from reconciliation.
2. `running` selects the running state.
3. `stopped` selects the stopped state.
4. Without an active override, the later of the most recent start and stop cron ticks wins. A tie selects stopped.

An override with `override_expires_at <= now` is inactive. Its stored attributes may remain in DynamoDB; the schedule takes over automatically.

A schedule is a pair of five-field cron expressions interpreted in `DEFAULT_TIMEZONE`. Both expressions must exist and must have reachable ticks. A group must have a schedule, an override, or both. A timed override additionally requires a schedule.

## Convergence and concurrency

Only stable resources whose observed state differs from the desired state receive an action. Transitional resources are skipped until a later cycle.

Invocations may overlap. A Start or Stop may therefore be delivered more than once, and invocations holding different configuration snapshots may briefly issue opposing actions. After older invocations finish, a successful later cycle converges to the latest configuration.

## Supported resources

- RDS DB instances that are not cluster members and do not use a `custom-*` engine
- RDS DB clusters whose engine starts with `aurora`
- ECS services using the `REPLICA` scheduling strategy
- EC2 instances

See [Resource tags](resource_tag.md) for ECS restoration settings.
