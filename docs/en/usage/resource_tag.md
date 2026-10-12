# Resource tags

This document defines membership and ECS configuration tags, including where they are set, their defaults, and value constraints.

## Membership tag

Set the group name on each managed AWS resource:

```text
cheapskate:group=<group-name>
```

Register a group with the same name in DynamoDB. Empty values, invalid names, and unregistered names are errors for the affected resource. For name constraints, see [Group configuration](group_config.md#group-names).

The reconciler discovers supported resources through Resource Groups Tagging API using the cheapskate:group key. Membership comes from the tag value returned with each ARN. ECS services require long ARNs containing the cluster name.

AWS assigns the aws:autoscaling:groupName tag to EC2 Auto Scaling group members. Instances with that tag are excluded from cheapskate management. For eligibility, see [Start and stop behavior](resource_flow.md#supported-resources-and-operations).

## ECS configuration tags

Set these tags on the ECS service itself. Register the group's ecs_max_count separately in DynamoDB.

| Tag | Default | Meaning | Constraint |
| --- | --- | --- | --- |
| cheapskate/desired-count | 1 | Startup capacity from all-zero counts or 0/0 bounds | Positive int32 |
| cheapskate/scaling-min | cheapskate/desired-count value | Normal MinCapacity | Nonnegative int32 |
| cheapskate/scaling-max | cheapskate/desired-count value | Normal MaxCapacity | Nonnegative int32 |
| cheapskate/scheduled-scaling-paused | false | Keep Scheduled Scaling suspended while running | Lowercase true or false |

Defaults apply only when a tag is absent. An empty string is invalid. After defaults, counts must satisfy cheapskate/scaling-min <= cheapskate/desired-count <= cheapskate/scaling-max.

All tags are validated before starting or stopping, whether or not a scalable target exists. Invalid tags prevent changes to that service. Startup and running configuration updates also require cheapskate/desired-count and cheapskate/scaling-max to be no greater than the group's ecs_max_count.

For application timing and scaling behavior, see [ECS startup, shutdown, and scaling](ecs.md). For limit registration and suspension procedures, see [Daily operations](operations.md#ecs-configuration-changes).
