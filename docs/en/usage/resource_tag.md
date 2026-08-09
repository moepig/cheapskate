# Configuration through AWS resource tags

This document specifies the tags applied to the managed AWS resources themselves. There are two kinds: selector tags, which decide what is managed, and parameter tags, which supply operating parameters. Their roles are given below.

| Kind | Key | Role |
|---|---|---|
| Selector tag | Chosen freely on the group side | A resource carrying it becomes managed by that group |
| Parameter tag | Fixed, starting with `cheapskate/` | Supplies an operating parameter. Everything works on defaults when unset |

For both, the only operation on the resource side is adding or removing a tag; nothing on the DynamoDB side is involved. The change takes effect from the next reconcile cycle.

## Selector tags

A group's selector is a tag key/value plus a set of resource types, and a resource matching both becomes managed.

```console
cheapskate-cli set-selector --group dev --tag-key cheapskate:group --tag-value dev --types rds-cluster,ecs-service
```

The tag to apply on the resource side for that configuration is `cheapskate:group=dev`. The key and value are arbitrary and need not come from `cheapskate`; an existing `Env=dev` and the like will do.

## Parameter tags

The tag keys recognized and their effects are given below.

| Tag key | Applies to | Effect |
| --- | --- | --- |
| `cheapskate/desired-count` | ecs-service | The desiredCount set at start. Unset means 1 |
| `cheapskate/scaling-min` | ecs-service | With Application Auto Scaling in use, the minimum capacity set at start. Unset means the same as desiredCount |
| `cheapskate/scaling-max` | ecs-service | With Application Auto Scaling in use, the maximum capacity set at start. Unset means the same as desiredCount |

These are tags on the resource rather than attributes on the group because one selector can match several ECS services.

### Rules for the values

The rules a value must satisfy are given below.

| Item | Rule |
|---|---|
| Type | A non-negative integer. An empty string counts as unset |
| `cheapskate/desired-count` | Must be greater than 0 |
| Relationship between the three | `scaling-min <= desired-count <= scaling-max` must hold |

A value breaking the rules becomes an error at start, and no AWS API is called. The error is recorded in `last_error` on `status#`. Allowing a desiredCount outside the range would let Auto Scaling pull the count straight back to the bound right after it was set, so the requested count would never hold.

The current values can be read from `resources[].config` in `cheapskate-cli show --group <name>` and on the group page of the web console.

### When to apply the tags

> [!IMPORTANT]
> Apply the tags before stopping the resource for the first time. Starting an ECS service restores from the tag values above, not from anything saved at stop. Stopping it with the tags unset makes the desiredCount at start 1 (with min/max the same), and the original values cannot be recovered.

```console
aws ecs tag-resource --resource-arn <service ARN> --tags \
  key=cheapskate:group,value=dev \
  key=cheapskate/desired-count,value=2 \
  key=cheapskate/scaling-min,value=1 \
  key=cheapskate/scaling-max,value=4
```
