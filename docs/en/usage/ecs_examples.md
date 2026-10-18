# ECS configuration and current-state examples

This document pairs ECS configuration values with observed AWS state in five examples. For startup, shutdown, and scaling rules, see [ECS startup, shutdown, and scaling](ecs.md).

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
| cheapskate:desired-count | 2 |
| cheapskate:scaling-min | 1 |
| cheapskate:scaling-max | 3 |
| cheapskate:scaling-bounds-mode | scheduled |

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
| cheapskate:desired-count | 2 | 1 |
| cheapskate:scaling-min | 2 | 1 |
| cheapskate:scaling-max | 2 | 3 |
| cheapskate:scaling-bounds-mode | tags | tags |

Applying normal bounds while retaining in-range capacity produces these observations:

| Point | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | Actual ECS situation |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Tags changed, settings not yet applied | 2 | 2 | 0 | 2 | 2 | true | Two tasks running in a fixed range |
| Normal bounds applied | 2 | 2 | 0 | 1 | 3 | true | Both running tasks retained |

Neither all-zero counts nor 0/0 bounds apply, so the new startup count of 1 is not used. Current desiredCount=2 is within the new range. The same AWS state left by a partial startup produces the same behavior.

### Resuming with capacity outside normal bounds

DynamoDB keeps override=running and ecs_max_count=3. Tags use the common values, including cheapskate:scaling-bounds-mode=scheduled. AWS has old bounds 4..6, suspension enabled, and five running tasks. Applying normal bounds and resuming can produce:

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

Bounds mode is scheduled and the AWS suspension flag is false, so the next cycle leaves bounds and capacity unchanged. DynamoDB ecs_max_count=3 validates cheapskate's tag-driven changes; it does not cap scheduled action bounds or current capacity.

### Stopping during startup

Change DynamoDB override from running to stopped. Leave ecs_max_count=3 and all service tags unchanged. Applying bounds 0/0 and suspension can produce:

| Point | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | Actual ECS situation |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Before stop settings | 2 | 1 | 1 | 2 | 2 | true | One task running; one pending |
| Stop settings applied | 0 | 1 | 0 | 0 | 0 | true | Pending task cancelled; running task draining |
| Tasks stopped | 0 | 0 | 0 | 0 | 0 | true | No running or pending tasks; stop settings satisfied |

The stop request takes precedence over the startup-count tag of 2 and scheduled bounds mode. Normal startup settings are not restored. Changing DynamoDB override to disabled instead skips AWS inspection and changes, retaining the settings present at that point.
