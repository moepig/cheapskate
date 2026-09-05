# Resource tags

## Group membership

Attach exactly one fixed membership tag to each managed resource:

```text
cheapskate:group=<group-name>
```

The reconciler calls Resource Groups Tagging API `GetResources` with `cheapskate:group` and no value filter, requests up to 100 resources per page, and follows every page. Membership is determined only by the tag value returned for each ARN.

## ECS restoration settings

ECS has no stopped state, so cheapskate scales a service to zero and later restores values from tags on that service.

| Tag | Required | Meaning |
| --- | --- | --- |
| `cheapskate/desired-count` | no; defaults to `1` | Desired task count after Start; positive int32 |
| `cheapskate/scaling-min` | no; defaults to desired count | Restored minimum capacity; non-negative int32 |
| `cheapskate/scaling-max` | no; defaults to desired count | Restored maximum capacity; non-negative int32 |

For a scalable target, all three values must satisfy `0 <= min <= desired <= max`. Stop changes only its bounds to `0/0`. Start restores min/max first and then desired count.

Without a scalable target, Stop sets desired count to zero, and Start restores the configured or default desired count. All three tag values are validated even when no scalable target exists.

The complete tag set is validated before any modifying ECS or Application Auto Scaling call. A service using any scheduling strategy other than `REPLICA` is rejected.
