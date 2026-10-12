# ECS startup, shutdown, and scaling

This document explains how ECS startup capacity, scaling bounds, and Scheduled Scaling suspension change in response to group requests and current AWS state. It covers REPLICA services.

## Configuration and current state

Group configuration, service tags, and AWS observations have separate purposes:

| Source | Fields | Purpose |
| --- | --- | --- |
| DynamoDB | start_cron, stop_cron, override, override_expires_at | Select running, stopped, or disabled using current time and DEFAULT_TIMEZONE |
| DynamoDB | ecs_max_count | Validate startup count and normal maximum applied by cheapskate |
| ECS service tags | cheapskate:group | Name of the DynamoDB group |
| ECS service tags | cheapskate/desired-count | Startup count from all-zero counts or 0/0 bounds |
| ECS service tags | cheapskate/scaling-min, cheapskate/scaling-max | Normal bounds applied at the end of startup, during suspension, and when resuming |
| ECS service tags | cheapskate/scheduled-scaling-paused | Requested Scheduled Scaling suspension |
| Current ECS state | desiredCount, runningCount, pendingCount | Configured, running, and pending task counts |
| Current Application Auto Scaling state | MinCapacity, MaxCapacity, ScheduledScalingSuspended | Applied bounds and suspension flag |

The group's running request means keeping the service active. It is distinct from current ECS desiredCount and the startup-count tag. The cheapskate/desired-count tag does not continuously fix running capacity.

For types and defaults, see [Group configuration](group_config.md#ecs-capacity-limit) and [Resource tags](resource_tag.md#ecs-configuration-tags).

## Stopping and starting

ECS is stopped only when desiredCount, runningCount, and pendingCount are all zero. Updating desiredCount and starting or stopping tasks are asynchronous; runningCount changes later. pendingCount counts tasks waiting to start, not tasks waiting to terminate.

### With a scalable target

A stopped group request applies MinCapacity=0, MaxCapacity=0, and ScheduledScalingSuspended=true. Suspension applies while stopped even when the tag requests false.

A running group request initializes capacity only when all ECS counts are zero or both bounds are zero. It first pins bounds to cheapskate/desired-count and suspends Scheduled Scaling. It reads ECS capacity again and changes desiredCount only if necessary. The final update applies normal bounds and the tag's suspension request together.

Completed startup configuration does not mean all tasks have started. Updates whose settings already match are omitted.

### Without a scalable target

Stopping sets desiredCount=0. Starting sets cheapskate/desired-count only when all counts are zero. An active service, including one with pending tasks, is not reset to startup capacity. A scalable target is not created.

## Scaling during normal operation

When a scalable target exists and capacity initialization is unnecessary, these rules apply:

| Suspension tag | Current AWS state | Behavior |
| --- | --- | --- |
| true | Normal bounds or suspension differ | Apply normal bounds and ScheduledScalingSuspended=true |
| true | Settings match | No changes |
| false or absent | ScheduledScalingSuspended=true | Apply normal bounds and ScheduledScalingSuspended=false |
| false or absent | ScheduledScalingSuspended=false | Retain current bounds and count |

Applying or clearing suspension retains desiredCount when it is within normal bounds. AWS adjusts capacity outside that range. The startup-count tag is not reapplied. For capacity adjustments, see [RegisterScalableTarget](https://docs.aws.amazon.com/autoscaling/application/APIReference/API_RegisterScalableTarget.html).

After resuming, Scheduled Scaling changes to bounds and counts are retained. Set the suspension request to true to continuously maintain normal tag bounds, including on a service without scheduled actions.

### Suspension scope

cheapskate/scheduled-scaling-paused=true persists across service starts and stops. cheapskate does not rewrite the tag. Clear the request by setting false or removing the tag.

Suspension prevents new scheduled actions from starting. It does not cancel an action already underway or replay actions whose execution times passed during suspension. Resuming enables future executions. See [AWS scaling suspension and resumption](https://docs.aws.amazon.com/autoscaling/application/userguide/application-auto-scaling-suspend-resume-scaling.html).

Scheduled action definitions, DynamicScalingInSuspended, and DynamicScalingOutSuspended are unchanged. For flag scope, see [SuspendedState](https://docs.aws.amazon.com/autoscaling/application/APIReference/API_SuspendedState.html).

### Capacity limits and remaining at zero

ecs_max_count validates the startup-count and normal-maximum tags applied by cheapskate. It is not an absolute limit on Scheduled Scaling bounds or current capacity.

If Scheduled Scaling reduces all counts to zero while the group requests running, the next cycle starts the service again. Use cheapskate's stop schedule or a stopped override for periods that must remain at zero.

## Request and current-state flow

For a registered scalable target, operations are selected as follows. Update values come from tags; stopped and suspended status come from current AWS state.

```mermaid
flowchart TD
    request{"Group request"} -->|disabled| skip["Skip inspection and changes"]
    request -->|running or stopped| observe["Inspect ECS desiredCount, runningCount, pendingCount<br/>and AWS MinCapacity, MaxCapacity,<br/>ScheduledScalingSuspended"]
    observe --> desired{"Group request"}
    desired -->|stopped| stop["Converge to MinCapacity=0, MaxCapacity=0,<br/>ScheduledScalingSuspended=true"]
    desired -->|running| initialize{"All counts zero or<br/>MinCapacity=MaxCapacity=0?"}
    initialize -->|Yes| start["Pin bounds to cheapskate/desired-count<br/>ScheduledScalingSuspended=true<br/>Set ECS desiredCount if needed"]
    start --> apply["Apply cheapskate/scaling-min and cheapskate/scaling-max<br/>and cheapskate/scheduled-scaling-paused<br/>Omit matching updates"]
    initialize -->|No| paused{"cheapskate/scheduled-scaling-paused<br/>true?"}
    paused -->|Yes| apply
    paused -->|No| resume{"AWS<br/>ScheduledScalingSuspended=true?"}
    resume -->|Yes| apply
    resume -->|No| keep["Retain current bounds and count"]
```

## Tag changes and partial failures

A failed operation leaves applied AWS settings in place. The next cycle reads group configuration, tags, and current state again. Neither DynamoDB nor tags store execution history or startup progress.

A positive fixed range can represent either normal configuration or partial startup. It is not used to infer an execution phase. Normal bounds and the suspension request are applied instead. Tag changes during partial startup retain applied capacity within the new normal bounds. A changed cheapskate/desired-count takes effect at the next start from all-zero counts or 0/0 bounds.

A later cycle that reads stopped or disabled follows that request instead of resuming old startup work. For overlapping invocation behavior, see [Start and stop behavior](resource_flow.md#failures-and-overlapping-invocations).

## Configuration and current-state examples

These examples use cluster dev, service api, and an existing scalable target. DynamoDB ecs_max_count is a Number; other attributes and all tags are Strings. No override_expires_at is registered, so the override is indefinite. The common DynamoDB item is:

| DynamoDB attribute | Registered value |
| --- | --- |
| pk | CONFIG |
| sk | GROUP#dev |
| override | running |
| ecs_max_count | 3 |

Unless a change is shown explicitly, the service tags are:

| ECS service tag | Registered value |
| --- | --- |
| cheapskate:group | dev |
| cheapskate/desired-count | 2 |
| cheapskate/scaling-min | 1 |
| cheapskate/scaling-max | 3 |
| cheapskate/scheduled-scaling-paused | false |

The tables show possible AWS observations. Updating configuration does not require runningCount to match immediately.

### Starting from zero with an intermediate failure

DynamoDB override=running selects startup. All counts and bounds are zero, so capacity is pinned to 2 and Scheduled Scaling stays suspended. Inspection fails before normal bounds are applied. A possible progression is:

| Point | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | Actual ECS situation |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Before startup | 0 | 0 | 0 | 0 | 0 | true | No tasks; stop settings remain |
| Failure after pinning | 2 | 0 | 2 | 2 | 2 | true | Two tasks pending; none running |
| Normal settings applied next cycle | 2 | 1 | 1 | 1 | 3 | false | One task running; one pending |
| Tasks started | 2 | 2 | 0 | 1 | 3 | false | Two tasks running |

The next cycle sees positive desiredCount and nonzero bounds. It applies normal bounds 1..3 and resumes without repinning startup capacity. It does not wait for every task to start.

### Changing tags on a running fixed range

DynamoDB keeps override=running and ecs_max_count=3. Membership stays cheapskate:group=dev. Change the other tags as follows:

| ECS service tag | Before | After |
| --- | --- | --- |
| cheapskate/desired-count | 2 | 1 |
| cheapskate/scaling-min | 2 | 1 |
| cheapskate/scaling-max | 2 | 3 |
| cheapskate/scheduled-scaling-paused | true | true |

Applying normal bounds while retaining in-range capacity produces these observations:

| Point | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | Actual ECS situation |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Tags changed, settings not yet applied | 2 | 2 | 0 | 2 | 2 | true | Two tasks running in a fixed range |
| Normal bounds applied | 2 | 2 | 0 | 1 | 3 | true | Both running tasks retained |

Neither all-zero counts nor 0/0 bounds apply, so the new startup count of 1 is not used. Current desiredCount=2 is within the new range. The same AWS state left by a partial startup produces the same behavior.

### Resuming with capacity outside normal bounds

DynamoDB keeps override=running and ecs_max_count=3. Tags use the common values, including a false suspension request. AWS has old bounds 4..6, suspension enabled, and five running tasks. Applying normal bounds and resuming can produce:

| Point | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | Actual ECS situation |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Before resuming | 5 | 5 | 0 | 4 | 6 | true | Five tasks running |
| Normal bounds and resume applied | 3 | 5 | 0 | 1 | 3 | false | Configured count is three; excess tasks still draining |
| Tasks stopped | 3 | 3 | 0 | 1 | 3 | false | Three tasks running |

AWS adjusts desiredCount to 3 within normal bounds 1..3. It is not reset to the startup-count tag of 2.

### Scheduled Scaling after a lost final response

DynamoDB and tags use the common values. Applying normal bounds 1..3 and ScheduledScalingSuspended=false succeeds, but its response is lost. A scheduled action then changes bounds to 4..6. Possible observations are:

| Point | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | Actual ECS situation |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Final update succeeds; response lost | 2 | 2 | 0 | 1 | 3 | false | Two running tasks; Scheduled Scaling enabled |
| Scheduled action changes bounds | 4 | 2 | 2 | 4 | 6 | false | Configured count rises to four; two tasks pending |
| Next cycle | 4 | 4 | 0 | 4 | 6 | false | Four tasks running |

Both the tag and AWS suspension flag are false, so the next cycle leaves bounds and capacity unchanged. DynamoDB ecs_max_count=3 validates cheapskate's tag-driven changes; it does not cap scheduled action bounds or current capacity.

### Stopping during startup

Change DynamoDB override from running to stopped. Leave ecs_max_count=3 and all service tags unchanged. Applying bounds 0/0 and suspension can produce:

| Point | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | Actual ECS situation |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Before stop settings | 2 | 1 | 1 | 2 | 2 | true | One task running; one pending |
| Stop settings applied | 0 | 1 | 0 | 0 | 0 | true | Pending task cancelled; running task draining |
| Tasks stopped | 0 | 0 | 0 | 0 | 0 | true | No running or pending tasks; stop settings satisfied |

The stop request takes precedence over the startup-count tag of 2 and false suspension request. Normal startup settings are not restored. Changing DynamoDB override to disabled instead skips AWS inspection and changes, retaining the settings present at that point.
