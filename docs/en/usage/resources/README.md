# Resource configuration and shared settings

This index links to resource-specific settings and shared configuration and operations. Choose a resource document for its tag location and eligibility requirements.

## Resource types

Choose the document for the resource being configured:

| Resource | cheapskate resource type name | Tag location | Specific settings |
| --- | --- | --- | --- |
| [RDS DB instance](rds-instance.md) | rds-instance | DB instance | None |
| [Aurora DB cluster](rds-cluster.md) | rds-cluster | DB cluster | None |
| [EC2 instance](ec2-instance.md) | ec2-instance | EC2 instance | Exclusion through the Auto Scaling group membership tag |
| [ECS service](ecs-service.md) | ecs-service | ECS service | Capacity limit, startup count, bounds, suspension |

For ECS, the capacity limit is a group setting. Service tags define the startup count, normal bounds, and Scheduled Scaling suspension mode.

## Shared configuration and operations

DynamoDB stores one item per group. The CLI saves settings; later reconciler cycles apply them to AWS. Use these references for shared settings and operations:

| Topic | Reference |
| --- | --- |
| <a id="shared-prerequisites"></a>Connections, time zones, contexts, and AWS profiles | [CLI example prerequisites](../cli_records.md#prerequisites) |
| Table and runtime preparation | [Setup](../setup.md) |
| <a id="shared-dynamodb-settings"></a>Attributes, valid values, and scope | [Group configuration](../group_config.md) |
| <a id="shared-cli-operations"></a>Registration, changes, and saved values | [CLI group configuration examples](../cli_records.md) |
| ECS capacity-limit operations | [ECS DynamoDB and CLI settings](ecs-service.md#dynamodb-and-cli) |
| <a id="shared-membership-tag"></a>Tag locations, values, and discovery | [Resource tags](../resource_tag.md) |
| <a id="shared-verification"></a>State and error inspection | [Daily operations](../operations.md#inspecting-configuration-and-current-state) |
| Output fields | [CLI output formats](../cli.md#output-formats) |
| Timing and failures | [Start and stop behavior](../resource_flow.md) |
| Steps to end management | [Ending management](../operations.md#ending-management) |

Each resource document above provides its target ARN and tagging command.
