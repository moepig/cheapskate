# Resource operation sequences

This document describes calls between the reconciler and resource adapters, AWS API branches, and configuration repair conditions. It includes inputs and observations needed to read the implementation. Diagrams omit retries performed within the AWS SDK.

## Common reconcile cycle

A cycle loads all groups and discovery results before processing resources in ARN order. A disabled group does not receive even an adapter Describe call. The sequence from configuration loading to operation results is:

```mermaid
sequenceDiagram
    participant C as Invoker
    participant R as reconciler
    participant D as DynamoDB
    participant T as Resource Groups Tagging API
    participant A as port.Target
    participant N as SNS
    C->>R: Periodic or manual invoke (payload unused)
    loop Every Query page
        R->>D: Query(CONFIG, ConsistentRead=true)
        D-->>R: Group records and next page key
    end
    R->>R: Validate groups and resolve schedule / override requests
    loop Every GetResources page
        R->>T: GetResources(cheapskate:group, supported resource type filters)
        T-->>R: ARNs, tags, next page token
        R->>R: Validate ARNs and retain the last tags for duplicates
    end
    Note over R,T: Global read or ARN parsing failure aborts<br/>the cycle before any Describe
    loop Each resource in ARN order
        alt Invalid membership or group
            R->>R: Record error and continue
        else Group request is disabled
            R->>R: Skip resource
        else Valid managed resource
            R->>A: Describe(resource with group EcsMaxCount)
            A-->>R: Observation or error
            alt Describe error
                R->>R: Record error and continue
            else Observation succeeded
                R->>R: Compare State with request, then check NeedsStart / NeedsStop
                alt No operation, transitioning, or not-found
                    R->>R: Skip operation
                else Operation or configuration repair required
                    R->>A: Start(resource) or Stop(resource)
                    A-->>R: nil or error
                    alt Operation error
                        R->>R: Record error and continue
                    else API calls succeeded
                        R->>R: Collect action and write structured log
                        opt Notifications configured
                            loop Until success, at most 2 attempts
                                R->>N: Publish(action notification)
                                N-->>R: Success or error
                            end
                        end
                    end
                end
            end
        end
    end
    R-->>C: Summary
```

Notification failure is logged without reversing a successful operation. Start or Stop success does not imply that the resource transition has completed; a later cycle observes it again. No action history, checkpoint, or AWS settings snapshot is written to DynamoDB.

For global failure boundaries and overlapping invocations, see [Reconcile implementation contracts](reconcile.md). For the implementation, see the [reconciler](../../../internal/app/reconcile/reconcile.go) and [resource discovery](../../../internal/aws/tagging/tagging.go).

## RDS DB instances

Instances with a nonempty DBClusterIdentifier or an engine beginning with custom- are rejected. The sequence from Describe to modifying APIs is:

```mermaid
sequenceDiagram
    participant R as reconciler
    participant A as RdsInstanceTarget
    participant S as RDS
    R->>A: Describe(resource)
    A->>S: DescribeDBInstances(DBInstanceIdentifier=Ref)
    S-->>A: DBInstances or error
    alt Cluster member or RDS Custom
        A-->>R: Error (no modifying API)
    else Eligible instance
        A-->>R: Observation or Describe error
        alt stopped with running request
            R->>A: Start(resource)
            A->>S: StartDBInstance(DBInstanceIdentifier=Ref)
            S-->>A: Response or error
            A-->>R: nil or error
        else running with stopped request
            R->>A: Stop(resource)
            A->>S: StopDBInstance(DBInstanceIdentifier=Ref)
            S-->>A: Response or error
            A-->>R: nil or error
        else Matching state, transitioning, not found, or Describe error
            Note over R,A: No Start / Stop call
        end
    end
```

DBInstanceStatus available maps to running, stopped to stopped, and every other status to transitioning. DBInstanceNotFoundFault, an empty response, or no instance with an observable status yields not-found. Entries with nil DBInstanceStatus are not used for state classification.

For the implementation, see the [RDS adapter](../../../internal/aws/compute/rds.go).

## Aurora DB clusters

Only clusters whose engine begins with aurora are eligible. The cluster sequence is:

```mermaid
sequenceDiagram
    participant R as reconciler
    participant A as RdsClusterTarget
    participant S as RDS
    R->>A: Describe(resource)
    A->>S: DescribeDBClusters(DBClusterIdentifier=Ref)
    S-->>A: DBClusters or error
    alt Engine is not Aurora or is nil
        A-->>R: Error (no modifying API)
    else Eligible cluster
        A-->>R: Observation or Describe error
        alt stopped with running request
            R->>A: Start(resource)
            A->>S: StartDBCluster(DBClusterIdentifier=Ref)
            S-->>A: Response or error
            A-->>R: nil or error
        else running with stopped request
            R->>A: Stop(resource)
            A->>S: StopDBCluster(DBClusterIdentifier=Ref)
            S-->>A: Response or error
            A-->>R: nil or error
        else Matching state, transitioning, not found, or Describe error
            Note over R,A: No Start / Stop call
        end
    end
```

Status available maps to running, stopped to stopped, and every other status to transitioning. DBClusterNotFoundFault, an empty response, or no cluster with an observable status yields not-found. Entries with nil Status are not used for state classification. No modifying API is called for member DB instances.

For the implementation, see the [RDS adapter](../../../internal/aws/compute/rds.go).

## EC2 instances

An instance is rejected if its DescribeInstances tags contain aws:autoscaling:groupName. The instance sequence is:

```mermaid
sequenceDiagram
    participant R as reconciler
    participant A as Ec2InstanceTarget
    participant S as EC2
    R->>A: Describe(resource)
    A->>S: DescribeInstances(InstanceIds=[Ref])
    S-->>A: Reservations or error
    alt Auto Scaling group membership tag exists
        A-->>R: Error (no modifying API)
    else Eligible instance
        A-->>R: Observation or Describe error
        alt stopped with running request
            R->>A: Start(resource)
            A->>S: StartInstances(InstanceIds=[Ref])
            S-->>A: Response or error
            A-->>R: nil or error
        else running with stopped request
            R->>A: Stop(resource)
            A->>S: StopInstances(InstanceIds=[Ref])
            S-->>A: Response or error
            A-->>R: nil or error
        else Matching state, transitioning, not found, or Describe error
            Note over R,A: No Start / Stop call
        end
    end
```

State.Name running and stopped map to the corresponding observations. terminated and InvalidInstanceID.NotFound map to not-found; other states map to transitioning. An empty response or only instances with nil State also yields not-found.

For the implementation, see the [EC2 adapter](../../../internal/aws/compute/ec2.go).

## ECS services

ECS observes scalable-target settings as well as task counts. Configuration tags come from the discovery snapshot and are validated again by Start and Stop. Tag values in the diagrams include defaults. An absent or nil AWS ScheduledScalingSuspended flag is treated as false.

The scalable target is identified by ServiceNamespace=ecs, ResourceId=service/{cluster}/{service}, and ScalableDimension=ecs:service:DesiredCount. Every RegisterScalableTarget specifies only ScheduledScalingSuspended in SuspendedState and omits DynamicScalingInSuspended and DynamicScalingOutSuspended. Scheduled action definitions are neither read nor written.

### Observation

The sequence returning task state and repair requests is:

```mermaid
sequenceDiagram
    participant Reconciler as reconciler
    participant Adapter as EcsServiceTarget
    participant EcsAPI as ECS
    participant AutoScaling as Application Auto Scaling
    Reconciler->>Adapter: Describe(resource)
    Adapter->>Adapter: Split Ref into cluster / service
    Adapter->>EcsAPI: DescribeServices(cluster, service)
    EcsAPI-->>Adapter: Services or error
    alt No ACTIVE service
        Adapter-->>Reconciler: State=not-found
    else Scheduling strategy is neither REPLICA nor empty
        Adapter-->>Reconciler: Error
    else Eligible service
        Adapter->>Adapter: Validate cheapskate/desired-count, cheapskate/scaling-min,<br/>cheapskate/scaling-max, and cheapskate/scheduled-scaling-paused
        Note over Adapter,AutoScaling: Invalid tags return an error here
        Adapter->>AutoScaling: DescribeScalableTargets(service target)
        AutoScaling-->>Adapter: Scalable target or no target
        Adapter->>Adapter: All desiredCount / runningCount / pendingCount zero means stopped, otherwise running
        opt Target exists
            Adapter->>Adapter: Set NeedsStart / NeedsStop from AWS bounds and ScheduledScalingSuspended
        end
        Adapter-->>Reconciler: Observation(State, Detail, NeedsStart, NeedsStop)
    end
```

Ref splitting failures and DescribeServices failures also return errors. Detail contains task counts and, when a target exists, bounds and actual ScheduledScalingSuspended. Without a target, neither NeedsStart nor NeedsStop is set.

Repair conditions are listed below. Both flags can be true; the reconciler uses only the flag corresponding to the group's desired state.

| Flag | Condition |
| --- | --- |
| NeedsStop | AWS bounds differ from 0/0 or ScheduledScalingSuspended=false |
| NeedsStart | All desiredCount / runningCount / pendingCount zero, or bounds 0/0 |
| NeedsStart | AWS ScheduledScalingSuspended differs from the cheapskate/scheduled-scaling-paused tag |
| NeedsStart | cheapskate/scheduled-scaling-paused=true and AWS bounds differ from the cheapskate/scaling-min and cheapskate/scaling-max tags |

A positive fixed range is not used to infer incomplete startup. With cheapskate/scheduled-scaling-paused=false during normal operation, Scheduled Scaling changes to bounds do not trigger repair.

### Stop

Stop validates tags and reads the target again. The stop sequence is:

```mermaid
sequenceDiagram
    participant Reconciler as reconciler
    participant Adapter as EcsServiceTarget
    participant EcsAPI as ECS
    participant AutoScaling as Application Auto Scaling
    Reconciler->>Adapter: Stop(resource)
    Adapter->>Adapter: Validate tags and split Ref
    Adapter->>AutoScaling: DescribeScalableTargets(service target)
    AutoScaling-->>Adapter: Scalable target or no target
    alt Target exists
        Adapter->>AutoScaling: RegisterScalableTarget(MinCapacity=0, MaxCapacity=0,<br/>ScheduledScalingSuspended=true)
        AutoScaling-->>Adapter: Response or error
    else No target
        Adapter->>EcsAPI: UpdateService(desiredCount=0)
        EcsAPI-->>Adapter: Response or error
    end
    Adapter-->>Reconciler: nil or error
```

Stop applies AWS ScheduledScalingSuspended=true regardless of the cheapskate/scheduled-scaling-paused tag and never changes the tag. It does not reject stopping based on ecs_max_count. With a target, there is no additional UpdateService or rollback.

### Startup initialization

Start validates Ref, tags, and ecs_max_count before rereading the service and target. The sequence for all-zero desiredCount / runningCount / pendingCount or bounds of 0/0 is:

```mermaid
sequenceDiagram
    participant Reconciler as reconciler
    participant Adapter as EcsServiceTarget
    participant EcsAPI as ECS
    participant AutoScaling as Application Auto Scaling
    Reconciler->>Adapter: Start(resource)
    Adapter->>Adapter: Validate Ref, tags, and ecs_max_count
    Note over Adapter,EcsAPI: Missing DynamoDB ecs_max_count, or cheapskate/desired-count<br/>or cheapskate/scaling-max above that limit, returns an error
    Adapter->>EcsAPI: DescribeServices(cluster, service)
    EcsAPI-->>Adapter: Current service
    Adapter->>AutoScaling: DescribeScalableTargets(service target)
    AutoScaling-->>Adapter: Scalable target or no target
    alt No target and all desiredCount / runningCount / pendingCount zero
        opt Current desiredCount differs from the cheapskate/desired-count tag
            Adapter->>EcsAPI: UpdateService(desiredCount=cheapskate/desired-count tag value)
            EcsAPI-->>Adapter: Response
        end
    else Target exists and initialization is required
        opt Bounds not pinned to cheapskate/desired-count,<br/>or ScheduledScalingSuspended=false
            Adapter->>AutoScaling: RegisterScalableTarget(MinCapacity and MaxCapacity set to<br/>cheapskate/desired-count tag value, ScheduledScalingSuspended=true)
            AutoScaling-->>Adapter: Response
        end
        Adapter->>EcsAPI: DescribeServices(cluster, service)
        EcsAPI-->>Adapter: desiredCount after pinning
        opt Current desiredCount differs from the cheapskate/desired-count tag
            Adapter->>EcsAPI: UpdateService(desiredCount=cheapskate/desired-count tag value)
            EcsAPI-->>Adapter: Response
        end
        opt Normal bounds tags differ from cheapskate/desired-count,<br/>or cheapskate/scheduled-scaling-paused=false
            Adapter->>AutoScaling: RegisterScalableTarget(MinCapacity=cheapskate/scaling-min tag value,<br/>MaxCapacity=cheapskate/scaling-max tag value,<br/>ScheduledScalingSuspended=cheapskate/scheduled-scaling-paused tag value)
            AutoScaling-->>Adapter: Response
        end
    end
    Adapter-->>Reconciler: nil
```

This diagram shows the successful path. Any API failure stops subsequent calls and returns an error. A missing service or unsupported strategy also returns an error. The second observation accounts for RegisterScalableTarget adjusting capacity to fit its bounds.

cheapskate/scheduled-scaling-paused=false resumes only in the final RegisterScalableTarget. If the tag is true and both normal bounds tags equal the startup-count tag, the initial pin also satisfies normal configuration, so the final RegisterScalableTarget is omitted. For running services that need no initialization, Start uses the following repair sequence.

### Running configuration repair

For a service that needs no startup initialization, Start applies normal bounds and suspension configuration. Without a target it makes no modifying API call. With a target, the sequence is:

```mermaid
sequenceDiagram
    participant Reconciler as reconciler
    participant Adapter as EcsServiceTarget
    participant EcsAPI as ECS
    participant AutoScaling as Application Auto Scaling
    Reconciler->>Adapter: Start(resource)
    Adapter->>Adapter: Validate Ref, tags, and ecs_max_count
    Adapter->>EcsAPI: DescribeServices(cluster, service)
    EcsAPI-->>Adapter: Current service
    Adapter->>AutoScaling: DescribeScalableTargets(service target)
    AutoScaling-->>Adapter: Current target
    Adapter->>Adapter: Confirm initialization is unnecessary
    alt cheapskate/scheduled-scaling-paused=true and suspension or normal bounds need repair
        Adapter->>AutoScaling: RegisterScalableTarget(MinCapacity=cheapskate/scaling-min tag value,<br/>MaxCapacity=cheapskate/scaling-max tag value, ScheduledScalingSuspended=true)
        AutoScaling-->>Adapter: Response or error
    else cheapskate/scheduled-scaling-paused=false and AWS ScheduledScalingSuspended=true
        Adapter->>AutoScaling: RegisterScalableTarget(MinCapacity=cheapskate/scaling-min tag value,<br/>MaxCapacity=cheapskate/scaling-max tag value, ScheduledScalingSuspended=false)
        AutoScaling-->>Adapter: Response or error
    else Settings already match
        Note over Adapter,AutoScaling: No modifying API call
    end
    Adapter-->>Reconciler: nil or error
```

UpdateService is not called, and desired count is not reset to startup capacity. Applying normal bounds through RegisterScalableTarget adjusts capacity outside that range. All-zero task counts or bounds of 0/0 select initialization instead of this sequence.

### Partial failures and later cycles

When startup pinning produces a range different from the normal tag bounds, the following ECS observation fails, and the next cycle observes positive desiredCount, processing follows this sequence:

```mermaid
sequenceDiagram
    participant Reconciler as reconciler
    participant Adapter as EcsServiceTarget
    participant EcsAPI as ECS
    participant AutoScaling as Application Auto Scaling
    Note over Reconciler,AutoScaling: First cycle (initialization in progress)
    Adapter->>AutoScaling: RegisterScalableTarget(MinCapacity and MaxCapacity set to<br/>cheapskate/desired-count tag value, ScheduledScalingSuspended=true)
    AutoScaling-->>Adapter: Success
    Adapter->>EcsAPI: DescribeServices(cluster, service)
    EcsAPI-->>Adapter: Error
    Adapter-->>Reconciler: Error (pinned range retained)
    Reconciler->>Reconciler: Record error (no action notification)
    Note over Reconciler,AutoScaling: Next successful cycle
    Reconciler->>Adapter: Describe(resource with current tags)
    Adapter->>EcsAPI: DescribeServices(cluster, service)
    EcsAPI-->>Adapter: Positive desiredCount, tasks starting or running
    Adapter->>AutoScaling: DescribeScalableTargets(service target)
    AutoScaling-->>Adapter: Positive fixed range, ScheduledScalingSuspended=true
    Adapter-->>Reconciler: Observation with NeedsStart=true
    Reconciler->>Adapter: Start(resource with current tags)
    Note over Adapter,AutoScaling: Run configuration repair<br/>Apply normal bounds and suspension request without resetting startup capacity
    Adapter-->>Reconciler: nil
    Reconciler->>Reconciler: Record action and notify if configured
```

No execution phase is stored. Each cycle chooses operations from configuration and observations. Failure states and subsequent repairs are:

| Remaining state | Later cycle |
| --- | --- |
| Stop configuration not applied | Repair AWS bounds or ScheduledScalingSuspended differences |
| All task counts zero or bounds 0/0 | Run startup initialization |
| Positive desiredCount and a suspended positive fixed range | Apply normal bounds and suspension request; retain applied capacity within those bounds |
| Normal bounds equal startup count, with only the suspension flag differing | Apply normal bounds and suspension request |
| Final response lost, cheapskate/scheduled-scaling-paused=true, normal settings match | No additional operation |
| Final response lost, cheapskate/scheduled-scaling-paused=false, Scheduled Scaling subsequently changed bounds | Preserve the changed range |

An invocation holding an old snapshot can apply an old request. A successful later cycle converges to the latest request after the old invocation finishes. Disabling management, exiting management, and stopping execution of cheapskate itself do not restore applied AWS flags or bounds.

For the implementation, see the [ECS adapter](../../../internal/aws/compute/ecs.go). For failure recovery and configuration persistence checks, see the [existing ECS tests](../../../internal/aws/compute/ecs_test.go) and [Scheduled Scaling tests](../../../internal/aws/compute/ecs_scheduled_scaling_test.go).
