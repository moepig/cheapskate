# Resource tags

## Group membership

Attach exactly one fixed membership tag to each managed resource:

```text
cheapskate:group=<group-name>
```

The reconciler calls Resource Groups Tagging API `GetResources` with `cheapskate:group` and no value filter, requests up to 100 resources per page, and follows every page. Resource type filters also restrict discovery to supported RDS instances, RDS clusters, ECS services, and EC2 instances. Membership is determined only by the tag value returned for each ARN. Duplicate ARNs retain the last returned tag set and are processed once per cycle. An ARN parsing failure aborts all discovery. ECS service ARNs must use the long format containing the cluster name.

## EC2 eligibility

EC2 instances belonging to an Auto Scaling group are unsupported. If `DescribeInstances` returns an `aws:autoscaling:groupName` tag, cheapskate reports an error for that instance and performs no start or stop operation, even when it has a `cheapskate:group` tag. Other resources continue to be processed.

AWS automatically adds this tag to Auto Scaling group members. See [Tag Auto Scaling groups and instances](https://docs.aws.amazon.com/autoscaling/ec2/userguide/ec2-auto-scaling-tagging.html) for the tagging lifecycle.

## ECS restoration settings

ECS has no stopped state, so cheapskate scales a service to zero and later restores values from tags on that service.

| Tag | Required | Meaning |
| --- | --- | --- |
| `cheapskate/desired-count` | no; defaults to `1` | Desired task count for startup initialization; positive int32 |
| `cheapskate/scaling-min` | no; defaults to desired count | Restored minimum capacity; non-negative int32 |
| `cheapskate/scaling-max` | no; defaults to desired count | Restored maximum capacity; non-negative int32 |
| `cheapskate/scheduled-scaling-paused` | no; defaults to `false` | Persistent Scheduled Scaling suspension request; lowercase `true` or `false` |

Start also requires a Number attribute named `ecs_max_count` in the group's registered item. After tag defaults are applied, desired count and scaling maximum must each be no greater than this limit. Missing limits or values above the limit fail before any modifying API call. Stop does not apply this limit. See [ECS task count limit](operations.md#ecs-task-count-limit) for registration.

The three capacity values must satisfy `0 <= min <= desired <= max`, with or without a scalable target. Defaults apply only to absent tags; empty values are errors. The suspension request is also validated without a target, and values other than `true` or `false` are errors.

The complete tag set is validated before any modifying ECS or Application Auto Scaling call. A service using any scheduling strategy other than `REPLICA` is rejected.

### Scheduled Scaling suspension

`cheapskate/scheduled-scaling-paused=true` requests suspension even while the ECS service is running. cheapskate never rewrites this tag or clears the request because the service starts, a schedule fires, or an override changes. Set the tag to `false` or remove it to clear the request.

The AWS `ScheduledScalingSuspended` flag combines this request with the group's desired service state. The values applied in a stable managed state are:

| Group desired state | Suspension request tag | `ScheduledScalingSuspended` |
| --- | --- | --- |
| `stopped` | Any | `true` |
| `running` | `true` | `true` |
| `running` | `false` or absent | `false` |
| `disabled` | Any | Unchanged |

Startup initialization also keeps the AWS flag `true` until the final configuration update applies the tag value. A `false` tag does not allow Scheduled Scaling while cheapskate's request to keep the ECS service stopped is active.

Scheduled action definitions are retained. `DynamicScalingInSuspended` and `DynamicScalingOutSuspended` are unchanged. Direct changes to the AWS flag are not adopted as a new request; the next successful reconcile reapplies the tag and group requests. See [SuspendedState](https://docs.aws.amazon.com/autoscaling/application/APIReference/API_SuspendedState.html) for the scope of each flag.

### Startup, shutdown, and capacity bounds

With a scalable target, Stop applies bounds of `0/0` and `ScheduledScalingSuspended=true` in one request. Start initializes capacity if all task counts are zero, the bounds are `0/0`, or a temporary startup range remains. It pins both bounds to the desired-count tag value while suspending Scheduled Scaling, rereads the count, and updates it only when necessary. The final update restores the normal tag bounds and applies the suspension request.

Capacity bounds during normal operation follow these rules:

| Suspension request tag | Bounds |
| --- | --- |
| `true` | Maintain the scaling-min and scaling-max tag values |
| `false` or absent | Allow Scheduled Scaling changes after startup initialization |

A bounds difference alone does not trigger Start while the request is `false`. Set the request to `true` if normal tag bounds must be continuously maintained, including on services without scheduled actions.

Changing `false` to `true` while running also restores the tag bounds. AWS may adjust desired count if it falls outside that range. Changing `true` to `false` preserves current bounds and clears only the AWS flag. Neither operation resets desired count to the startup tag value. A flag-only update can still adjust capacity to fit the current bounds; see [RegisterScalableTarget](https://docs.aws.amazon.com/autoscaling/application/APIReference/API_RegisterScalableTarget.html).

Without a scalable target, Stop sets desired count to zero and Start restores its configured or default value. cheapskate does not create a target for suspension control. If a target is created later, the next reconcile applies the request.

### Observation and recovery

ECS is classified as stopped only when desired, running, and pending counts are all zero; otherwise it is running. Stop is reapplied for a stopped request if bounds differ from `0/0` or the AWS flag is `false`. Start is reapplied for a running request if initialization is incomplete, the AWS flag differs from the tag, or the request is `true` and bounds differ from the normal tags. A running desired-count difference alone does not trigger Start.

An intermediate failure leaves the applied AWS settings in place. A suspended positive fixed range that differs from the normal tag bounds is treated as incomplete startup and recovered using the current tags. If normal bounds equal startup capacity, only a remaining flag difference may need repair. A lost final response requires no further operation when settings have already converged. Notifications may be lost.

If Scheduled Scaling reduces all task counts to zero while the group desires `running`, the next reconcile starts the service again. Use cheapskate schedules or a `stopped` override for periods that must remain at zero.

For configuration propagation and management exit procedures, see [Operations](operations.md#ecs-scheduled-scaling-suspension).
