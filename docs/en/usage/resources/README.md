# Resource configuration and shared settings

This index links to resource-specific settings and shared configuration and operations. Choose a resource document for its tag location and eligibility requirements.

## Resource types

Choose the document for the resource being configured:

| Resource | cheapskate resource type name | Tag location | Specific settings |
| --- | --- | --- | --- |
| [RDS DB instance](rds-instance.md) | rds-instance | DB instance | None |
| [Aurora DB cluster](rds-cluster.md) | rds-cluster | DB cluster | None |
| [EC2 instance](ec2-instance.md) | ec2-instance | EC2 instance | Exclusion through the Auto Scaling group membership tag |
| [ECS service](ecs-service.md) | ecs-service | ECS service | Group capacity limit, startup count, normal bounds, and Scheduled Scaling suspension |

## Shared prerequisites

For connections, time zones, CLI contexts, and matching AWS profiles, see [CLI example prerequisites](../cli_records.md#prerequisites). For infrastructure preparation, see [Setup](../setup.md).

## Shared DynamoDB settings

DynamoDB stores one item per group. For attributes, valid values, and how a group affects its resources, see [Group configuration](../group_config.md).

## Shared CLI operations

For registration and changes paired with their saved values, see [CLI group configuration examples](../cli_records.md). For ECS capacity-limit operations, see [ECS DynamoDB and CLI settings](ecs-service.md#dynamodb-and-cli).

## Shared membership tag

For tag locations, value constraints, and discovery, see [Resource tags](../resource_tag.md). Each resource document above provides its target ARN and tagging command.

## Shared verification

The CLI saves settings; later reconciler cycles apply them to AWS. For state and error inspection steps, see [Daily operations](../operations.md#inspecting-configuration-and-current-state). For field definitions, see [CLI output formats](../cli.md#output-formats).

For timing and failures, see [Start and stop behavior](../resource_flow.md). To stop managing resources, follow [Ending management](../operations.md#ending-management).
