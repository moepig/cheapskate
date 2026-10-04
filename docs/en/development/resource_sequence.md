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
        R->>T: GetResources(cheapskate:group, supported type filters)
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

For global failure boundaries and overlapping invocations, see [Reconcile boundaries](reconcile.md). For the implementation, see the [reconciler](../../../internal/app/reconcile/reconcile.go) and [resource discovery](../../../internal/aws/tagging/tagging.go).

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

ECS observes scalable-target settings as well as task counts. Configuration tags come from the discovery snapshot and are validated again by Start and Stop. The following symbols are used in the sequences:

| Symbol | Meaning |
| --- | --- |
| D | Startup desired-count after tag defaults |
| L / U | Normal scaling-min / scaling-max after tag defaults |
| P | scheduled-scaling-paused request; absent means false |
| S | AWS ScheduledScalingSuspended; absent or nil means false |

The scalable target is identified by ServiceNamespace=ecs, ResourceId=service/{cluster}/{service}, and ScalableDimension=ecs:service:DesiredCount. Every RegisterScalableTarget specifies only ScheduledScalingSuspended and omits DynamicScalingInSuspended and DynamicScalingOutSuspended. Scheduled action definitions are neither read nor written.

### Observation

The sequence returning task state and repair requests is:

```mermaid
sequenceDiagram
    participant R as reconciler
    participant A as EcsServiceTarget
    participant E as ECS
    participant S as Application Auto Scaling
    R->>A: Describe(resource)
    A->>A: Split Ref into cluster / service
    A->>E: DescribeServices(cluster, service)
    E-->>A: Services or error
    alt No ACTIVE service
        A-->>R: State=not-found
    else Scheduling strategy is neither REPLICA nor empty
        A-->>R: Error
    else Eligible service
        A->>A: Parse and validate D, L, U, P from tags
        Note over A,S: Invalid tags return an error here
        A->>S: DescribeScalableTargets(service target)
        S-->>A: Scalable target or no target
        A->>A: All desired / running / pending counts zero means stopped, otherwise running
        opt Target exists
            A->>A: Set NeedsStart / NeedsStop from bounds and S
        end
        A-->>R: Observation(State, Detail, NeedsStart, NeedsStop)
    end
```

Ref splitting failures and DescribeServices failures also return errors. Detail contains task counts and, when a target exists, bounds and actual S. Without a target, neither NeedsStart nor NeedsStop is set.

Repair conditions are listed below. Both flags can be true; the reconciler uses only the flag corresponding to the group's desired state.

| Flag | Condition |
| --- | --- |
| NeedsStop | Bounds differ from 0/0 or S=false |
| NeedsStart | All task counts zero, bounds 0/0, or an incomplete startup range |
| NeedsStart | S differs from P |
| NeedsStart | P=true and bounds differ from L/U |

An incomplete startup range has S=true, positive equal minimum and maximum, and differs from L/U. The fixed value need not match current D. With P=false during normal operation, Scheduled Scaling changes to bounds do not trigger repair.

### Stop

Stop validates tags and reads the target again. The stop sequence is:

```mermaid
sequenceDiagram
    participant R as reconciler
    participant A as EcsServiceTarget
    participant E as ECS
    participant S as Application Auto Scaling
    R->>A: Stop(resource)
    A->>A: Validate tags and split Ref
    A->>S: DescribeScalableTargets(service target)
    S-->>A: Scalable target or no target
    alt Target exists
        A->>S: RegisterScalableTarget(min=0, max=0, S=true)
        S-->>A: Response or error
    else No target
        A->>E: UpdateService(desiredCount=0)
        E-->>A: Response or error
    end
    A-->>R: nil or error
```

Stop applies S=true regardless of P and never changes P. It does not reject stopping based on ecs_max_count. With a target, there is no additional UpdateService or rollback.

### Startup initialization

Start validates Ref, tags, and ecs_max_count before rereading the service and target. The sequence for a stopped service, bounds of 0/0, or an incomplete startup range is:

```mermaid
sequenceDiagram
    participant R as reconciler
    participant A as EcsServiceTarget
    participant E as ECS
    participant S as Application Auto Scaling
    R->>A: Start(resource)
    A->>A: Validate Ref, tags, and ecs_max_count
    Note over A,E: Missing limit, or D or U above the limit,<br/>returns an error before Start calls AWS
    A->>E: DescribeServices(cluster, service)
    E-->>A: Current service
    A->>S: DescribeScalableTargets(service target)
    S-->>A: Scalable target or no target
    alt No target
        opt Current desired count differs from D
            A->>E: UpdateService(desiredCount=D)
            E-->>A: Response
        end
    else Target exists and initialization is required
        opt Bounds differ from D/D or S=false
            A->>S: RegisterScalableTarget(min=D, max=D, S=true)
            S-->>A: Response
        end
        A->>E: DescribeServices(cluster, service)
        E-->>A: Desired count after pinning
        opt Current desired count differs from D
            A->>E: UpdateService(desiredCount=D)
            E-->>A: Response
        end
        opt Normal bounds differ from D/D or P=false
            A->>S: RegisterScalableTarget(min=L, max=U, S=P)
            S-->>A: Response
        end
    end
    A-->>R: nil
```

This diagram shows the successful path. Any API failure stops subsequent calls and returns an error. A missing service or unsupported strategy also returns an error. The second observation accounts for RegisterScalableTarget adjusting capacity to fit its bounds.

P=false resumes only in the final RegisterScalableTarget. If P=true and L=U=D, the initial pin also satisfies normal configuration, so the final RegisterScalableTarget is omitted. For running services that need no initialization, Start uses the following repair sequence.

### Running configuration repair

For a service that needs no startup initialization, Start reapplies only suspension configuration. The sequence is:

```mermaid
sequenceDiagram
    participant R as reconciler
    participant A as EcsServiceTarget
    participant E as ECS
    participant S as Application Auto Scaling
    R->>A: Start(resource)
    A->>A: Validate Ref, tags, and ecs_max_count
    A->>E: DescribeServices(cluster, service)
    E-->>A: Current service
    A->>S: DescribeScalableTargets(service target)
    S-->>A: Current target
    A->>A: Confirm initialization is unnecessary
    alt P=true and suspension or normal bounds need repair
        A->>S: RegisterScalableTarget(min=L, max=U, S=true)
        S-->>A: Response or error
    else P=false and S=true
        A->>S: RegisterScalableTarget(S=false, bounds omitted)
        S-->>A: Response or error
    else Settings already match
        Note over A,S: No modifying API call
    end
    A-->>R: nil or error
```

UpdateService is not called, and desired count is not reset to startup capacity. A flag-only RegisterScalableTarget can still adjust capacity if it is outside current bounds. Bounds of 0/0 or an incomplete startup range select initialization instead of this sequence.

### Partial failures and later cycles

When startup pinning produces a range different from the normal tag bounds and the following ECS observation fails, recovery follows this sequence:

```mermaid
sequenceDiagram
    participant R as reconciler
    participant A as EcsServiceTarget
    participant E as ECS
    participant S as Application Auto Scaling
    Note over R,S: First cycle (initialization in progress)
    A->>S: RegisterScalableTarget(min=D, max=D, S=true)
    S-->>A: Success
    A->>E: DescribeServices(cluster, service)
    E-->>A: Error
    A-->>R: Error (pinned range retained)
    R->>R: Record error (no action notification)
    Note over R,S: Next successful cycle
    R->>A: Describe(resource with current tags)
    A->>E: DescribeServices(cluster, service)
    E-->>A: Current task counts
    A->>S: DescribeScalableTargets(service target)
    S-->>A: Positive fixed range, S=true
    A-->>R: Observation with NeedsStart=true
    R->>A: Start(resource with current tags)
    Note over A,S: Repeat startup initialization sequence<br/>Apply only necessary pinning, count, and final settings
    A-->>R: nil
    R->>R: Record action and notify if configured
```

A true suspension request alone is not evidence of incomplete startup. Failure states and subsequent repairs are:

| Remaining state | Later cycle |
| --- | --- |
| Stop configuration not applied | Repair bounds or S differences |
| Incomplete startup range | Initialize using current D, L/U, and P |
| L=U=D with only S differing from the request | Repair the flag difference |
| Final response lost, P=true, normal settings match | No additional operation |
| Final response lost, P=false, Scheduled Scaling subsequently changed bounds | Preserve the changed range |

An invocation holding an old snapshot can apply an old request. A successful later cycle converges to the latest request after the old invocation finishes. Disabling management, exiting management, and stopping execution of cheapskate itself do not restore applied AWS flags or bounds.

For the implementation, see the [ECS adapter](../../../internal/aws/compute/ecs.go). For failure recovery and configuration persistence checks, see the [existing ECS tests](../../../internal/aws/compute/ecs_test.go) and [Scheduled Scaling tests](../../../internal/aws/compute/ecs_scheduled_scaling_test.go).
