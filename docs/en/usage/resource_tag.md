# Resource tags

## Group membership

Attach exactly one fixed membership tag to each managed resource:

```text
cheapskate:group=<group-name>
```

The reconciler calls Resource Groups Tagging API `GetResources` with `cheapskate:group` and no value filter, requests up to 100 resources per page, and follows every page. Resource type filters also restrict discovery to supported RDS instances, RDS clusters, ECS services, and EC2 instances. Membership is determined only by the tag value returned for each ARN. Duplicate ARNs retain the last returned tag set and are processed once per cycle. An ARN parsing failure aborts all discovery. ECS service ARNs must use the long format containing the cluster name.

## ECS restoration settings

ECS has no stopped state, so cheapskate scales a service to zero and later restores values from tags on that service.

| Tag | Required | Meaning |
| --- | --- | --- |
| `cheapskate/desired-count` | no; defaults to `1` | Desired task count after Start; positive int32 |
| `cheapskate/scaling-min` | no; defaults to desired count | Restored minimum capacity; non-negative int32 |
| `cheapskate/scaling-max` | no; defaults to desired count | Restored maximum capacity; non-negative int32 |

All three values must satisfy `0 <= min <= desired <= max`, with or without a scalable target. Defaults apply only to absent tags; empty values are errors.

With a scalable target, Stop changes only its bounds to `0/0`. Start temporarily sets both bounds to the desired-count tag value, rereads the count, and updates it only when it differs from that value. Once the count is set successfully, Start restores the configured min/max bounds.

An intermediate failure leaves the temporary bounds in place. Their difference from the configured bounds lets the next reconcile resume the start operation. When the configured bounds also equal the desired count, setting the bounds already adjusts capacity to the starting count, so the service is converged. If restoring the configured bounds succeeds but its response is lost, the next reconcile also treats the service as converged. Notifications may be lost.

Updating an existing scalable target adjusts its capacity to fit the bounds. See [RegisterScalableTarget](https://docs.aws.amazon.com/autoscaling/application/APIReference/API_RegisterScalableTarget.html) for the API behavior.

Without a scalable target, Stop sets desired count to zero, and Start restores the configured or default desired count. All three tag values are validated even when no scalable target exists.

The complete tag set is validated before any modifying ECS or Application Auto Scaling call. A service using any scheduling strategy other than `REPLICA` is rejected.

ECS is classified as stopped only when desired, running, and pending counts are all zero; otherwise it is running. Matching running state still triggers Start when scalable-target bounds differ from the tags. Matching stopped state still triggers Stop when bounds are not `0/0`. When a running service has matching bounds or no scalable target, a desired-count difference alone does not trigger Start.
