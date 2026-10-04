# Resource processing flows

This document outlines how cheapskate starts and stops RDS DB instances, Aurora DB clusters, EC2 instances, and ECS services.

## Common processing

cheapskate normally runs every five minutes and derives resource requests from group configuration and membership tags. A failure to load all group configuration or discover all resources prevents resource operations in that cycle. Invalid membership or group configuration is reported as a resource error.

Group requests have the following effects:

| Request | Behavior |
| --- | --- |
| `running` | Converge the managed resource to its running state |
| `stopped` | Converge the managed resource to its stopped state |
| `disabled` | Do not observe, start, or stop the resource |

The following diagrams describe one cycle for a resource with automatic control enabled. Starting and stopping refer to the managed AWS resource. Stopping execution of cheapskate itself leaves applied AWS settings in place and prevents new requests and repairs from being applied.

A start or stop operation ends for that cycle when AWS accepts it. cheapskate does not wait for the resource transition to finish; a later cycle observes its state again. After an operation fails, later cycles also compare the current request and resource state again.

For schedule and override resolution, see [Concepts](concepts.md).

## RDS DB instances

Eligible instances are not cluster members and do not use RDS Custom. The instance flow is:

```mermaid
flowchart TD
    observe["Observe DB instance state and configuration"] --> eligible{"Cluster member or RDS Custom?"}
    eligible -->|Yes| error["Resource error<br/>Do not start or stop"]
    eligible -->|No| decide{"Current state and group request"}
    decide -->|Stopped with running request| start["Start DB instance"]
    decide -->|Running with stopped request| stop["Stop DB instance"]
    decide -->|State matches request| keep["No operation"]
    decide -->|Transitioning or not found| skip["Skip this cycle"]
    start --> next["Observe again in a later cycle"]
    stop --> next
    keep --> next
    skip --> next
    error --> next
```

RDS status `available` is treated as running, `stopped` as stopped, and other statuses as transitioning.

## Aurora DB clusters

Aurora is managed as a DB cluster. Do not register its member DB instances as individual stop targets. The cluster flow is:

```mermaid
flowchart TD
    observe["Observe DB cluster state and engine"] --> eligible{"Aurora cluster?"}
    eligible -->|No| error["Resource error<br/>Do not start or stop"]
    eligible -->|Yes| decide{"Current state and group request"}
    decide -->|Stopped with running request| start["Start DB cluster"]
    decide -->|Running with stopped request| stop["Stop DB cluster"]
    decide -->|State matches request| keep["No operation"]
    decide -->|Transitioning or not found| skip["Skip this cycle"]
    start --> next["Observe again in a later cycle"]
    stop --> next
    keep --> next
    skip --> next
    error --> next
```

RDS status `available` is treated as running, `stopped` as stopped, and other statuses as transitioning.

## EC2 instances

Eligible instances do not belong to an Auto Scaling group. The instance flow is:

```mermaid
flowchart TD
    observe["Observe EC2 state and group membership"] --> eligible{"Auto Scaling group member?"}
    eligible -->|Yes| error["Resource error<br/>Do not start or stop"]
    eligible -->|No| decide{"Current state and group request"}
    decide -->|Stopped with running request| start["Start EC2 instance"]
    decide -->|Running with stopped request| stop["Stop EC2 instance"]
    decide -->|State matches request| keep["No operation"]
    decide -->|Transitioning or not found| skip["Skip this cycle"]
    start --> next["Observe again in a later cycle"]
    stop --> next
    keep --> next
    skip --> next
    error --> next
```

`running` and `stopped` map to their corresponding states. `terminated` is treated as not found and is never restarted. Instances that are starting or stopping are skipped.

## ECS services

Stopping an ECS service means reducing its task count to zero. It is considered stopped only when desired, running, and pending counts are all zero. Otherwise it is considered running, and stop configuration can be applied even while tasks are draining.

Only `REPLICA` services are eligible. Invalid configuration tags prevent starts and stops. Both startup initialization and running configuration changes require validation against the group's `ecs_max_count` limit.

### Operations and configuration repair

In addition to task counts, ECS processing observes Scheduled Scaling suspension and capacity bounds. Its branches are:

```mermaid
flowchart TD
    observe["Observe task counts and scaling settings"] --> exists{"Service found?"}
    exists -->|No| next["Observe again in a later cycle"]
    exists -->|Yes| valid{"Service and configuration tags valid?"}
    valid -->|No| error["Resource error<br/>Do not start or stop"]
    valid -->|Yes| desired{"Group request"}
    desired -->|stopped| stopNeeded{"Task counts or stop settings need repair?"}
    stopNeeded -->|No| next
    stopNeeded -->|Yes| stop["Reduce task count to zero<br/>Suspend Scheduled Scaling if present"]
    desired -->|running| initialize{"Stopped, stop settings remain, or startup incomplete?"}
    initialize -->|Yes| start["Run startup initialization flow"]
    initialize -->|No| target{"Scalable target exists?"}
    target -->|No| next
    target -->|Yes| paused{"Suspension request tag true?"}
    paused -->|Yes| pauseNeeded{"Suspension and normal bounds already applied?"}
    pauseNeeded -->|Yes| next
    pauseNeeded -->|No| pause["Suspend Scheduled Scaling<br/>Restore normal tag bounds"]
    paused -->|No| resumeNeeded{"Scheduled Scaling needs resuming?"}
    resumeNeeded -->|No| next
    resumeNeeded -->|Yes| resume["Resume Scheduled Scaling<br/>Preserve current bounds"]
    stop --> next
    start --> next
    pause --> next
    resume --> next
    error --> next
```

With a scalable target, stop settings are bounds of `0/0` and Scheduled Scaling suspension. Without a target, only task count changes. Even with a `false` tag, Scheduled Scaling stays suspended while cheapskate requests that the ECS service remain stopped.

During normal operation with a `true` request, normal tag bounds are maintained. With a `false` request, Scheduled Scaling may change bounds. Running configuration changes do not reset desired count to startup capacity, although applying bounds may adjust a count outside that range.

Without a scalable target, suspension repair is unnecessary. A suspended positive fixed range that differs from normal tag bounds is treated as incomplete startup.

### Startup initialization

The startup flow for a stopped service, bounds of `0/0`, or an incomplete startup range is:

```mermaid
flowchart TD
    validate["Validate startup count, normal bounds, and group limit"] --> target{"Scalable target exists?"}
    target -->|No| countOnly["Set task count to startup tag value"]
    target -->|Yes| pin["Suspend Scheduled Scaling<br/>Pin bounds to startup count"]
    pin --> count["Check task count<br/>Set startup count if needed"]
    count --> bounds["Restore normal tag bounds"]
    bounds --> paused{"Suspension request tag true?"}
    paused -->|Yes| keep["Keep Scheduled Scaling suspended"]
    paused -->|No| resume["Resume Scheduled Scaling<br/>in the final startup update"]
    countOnly --> next["Observe again in a later cycle"]
    keep --> next
    resume --> next
```

The normal bounds and final suspension request shown in the diagram are applied together. Completing startup configuration does not mean all tasks have finished starting. An intermediate failure leaves applied settings in place so a later cycle can recover using the current tags.

A `true` suspension request persists across repeated service starts and stops. Scheduled Scaling suspension does not suspend every kind of automatic scaling while the service is running.

If Scheduled Scaling reduces all task counts to zero while the group requests `running`, a later cycle starts the service again. Use cheapskate schedules or a `stopped` override for periods that must remain at zero.

For configuration tags and bounds ownership, see [Resource tags](resource_tag.md#ecs-restoration-settings). For suspension release and management exit procedures, see [Operations](operations.md#ecs-scheduled-scaling-suspension).
