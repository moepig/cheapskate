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

Startup count, normal bounds, and the Scheduled Scaling suspension request are tags on the ECS service itself. For tags, defaults, constraints, and commands, see [ECS service resource tags](resources/ecs-service.md#resource-tags).

For application timing and scaling behavior, see [ECS startup, shutdown, and scaling](ecs.md).
