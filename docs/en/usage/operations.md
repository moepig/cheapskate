# Operations

`cheapskate-cli` and the web console use the same validation and conditional-write service.

## CLI

The global options are `-table <name>` and `-output text|json`.

```console
cheapskate-cli list
cheapskate-cli show --group dev
cheapskate-cli schedule --group dev -start '0 8 * * 1-5' -stop '0 20 * * 1-5'
cheapskate-cli override --group dev running
cheapskate-cli override --group dev stopped -for 2h
cheapskate-cli override --group dev disabled
cheapskate-cli clear-override --group dev
cheapskate-cli remove --group dev
```

`schedule` creates a group when absent or changes only its two cron attributes. An indefinite `override` can also create a group and changes only override attributes. A timed override requires an existing schedule. `clear-override` also requires a schedule. `remove` conditionally deletes the complete group item.

Every change first performs a strongly consistent read and checks unknown attributes and attribute types. Schedule, override, and clear-override validate the complete resulting group. Remove deletes a decodable item without validating cron expressions or cross-attribute invariants. A schedule change is rejected while an expired override remains because the resulting expiry is in the past; clear or replace that override first.

A conditional-write conflict is returned without an automatic retry. Changes to schedule and override attributes preserve each other; two writes to the same attribute use DynamoDB's last-applied value.

Do not delete and recreate a group under the same name concurrently with another configuration change. Write conditions check attribute existence only, so a stale request can change or delete the recreated group.

Exit codes are:

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Arguments, AWS, DynamoDB, or internal failure |
| `2` | Invalid stored group or conditional-write conflict |

Text `list` writes valid groups to stdout and invalid rows to stderr. When the list read succeeds, JSON list writes one complete object containing groups and errors to stdout. A failure of the read itself goes to stderr and returns exit code 1. Either form exits with code 2 when an invalid row exists. JSON `show` emits a structured error object for invalid stored data and returns code 2.

Validation failure of a proposed group change returns exit code 1.

Show returns exit code 0 even when individual resource Describe calls fail. JSON includes the cause in each affected resource’s live_error, so inspect observations rather than exit status alone before ending management. CLI expiry timestamps use UTC RFC 3339 regardless of DEFAULT_TIMEZONE.

## ECS task count limit

Register a Number attribute named `ecs_max_count` on the group item before allowing ECS starts. This limit applies to both desired count and scaling maximum for each ECS service and is stored independently of resource tags. Existing ECS groups also require this attribute. See [DynamoDB data model](../architecture/database.md) for its constraints.

For example, register a limit of 4 on an existing dev group:

```sh
aws dynamodb update-item \
  --table-name cheapskate \
  --key '{"pk":{"S":"CONFIG"},"sk":{"S":"GROUP#dev"}}' \
  --update-expression 'SET ecs_max_count = :maximum' \
  --expression-attribute-values '{":maximum":{"N":"4"}}' \
  --condition-expression 'attribute_exists(pk)'
```

Manage this attribute through DynamoDB or IaC. Schedule, override, and clear-override operations in the CLI and web console preserve it. CLI list/show and the web console's detail page display the saved limit.

Scheduled action bounds do not pass through the `ecs_max_count` check. This attribute is not an absolute limit on Scheduled Scaling capacity; also review the bounds of each scheduled action.

## ECS Scheduled Scaling suspension

Set `cheapskate/scheduled-scaling-paused=true` on the ECS service to keep Scheduled Scaling suspended while the service is running. The request persists across service starts and stops. See [Resource tags](resource_tag.md#scheduled-scaling-suspension) for the relationship between this tag and the AWS flag.

To clear a persistent suspension request:

1. Set the tag to `false` or remove it.
2. Set the desired ECS service state to `running`.
3. After a successful reconcile, confirm `ScheduledScalingSuspended=false` using JSON `show` output or the web console's live observation.

While the group desires the ECS service stopped, clearing the tag still leaves the AWS flag `true`. Resuming does not replay scheduled actions whose execution time passed during suspension; only future actions run. See [AWS resume behavior](https://docs.aws.amazon.com/autoscaling/application/userguide/application-auto-scaling-suspend-resume-scaling.html).

Tag changes take effect in a successful reconcile. Invocations with an older configuration snapshot may apply older requests. The AWS flag prevents new scheduled actions from starting; it does not cancel operations already underway.

Stopping execution of cheapskate itself does not change the ECS service or the AWS flag. Applied AWS settings remain, but tag changes and external AWS changes cannot be reconciled until execution resumes.

To retain an existing AWS suspension, set the tag to `true` before deploying a version with suspension control; the default for an absent tag is `false`. An existing suspended positive fixed range that differs from the normal tag bounds is treated as incomplete startup. Declare that range in the normal bounds tags or verify that reinitializing startup capacity is acceptable.

## Web console

The web console provides group list and detail pages plus schedule and override forms. Detail pages show `cheapskate:group=<group>`, ECS configuration tags, and current read-only observations. Dates are displayed and parsed in `DEFAULT_TIMEZONE`. A write conflict returns HTTP 409. The detail form initializes Until to two hours after page rendering; clear it to set an indefinite override. The index override form creates indefinite overrides.

## Safely ending management

To exit management with ECS Scheduled Scaling enabled, clear the suspension request before this sequence and select a `running` override. To exit with a running service and Scheduled Scaling suspended, set the tag to `true`. Exiting with the ECS service stopped leaves the AWS flag `true` regardless of the tag. Disabling management, removing membership tags, or removing the group does not restore AWS flags or bounds.

To leave resources in a known state:

1. Set an indefinite `running` or `stopped` override.
2. Wait at least one configured reconciler Lambda timeout after the write completes.
3. Invoke the reconciler manually or wait for the next periodic invocation.
4. Use `show` to confirm that every resource is stable in the selected state. For ECS, also inspect the AWS suspension flag and bounds in JSON live.Detail or the web console.
5. Remove the membership tags from the resources.
6. Remove the group.

Do not change that group again during this sequence. Waiting lets invocations that read older configuration finish before management ends.
