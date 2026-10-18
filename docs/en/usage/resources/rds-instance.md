# RDS DB instance configuration

This document describes eligibility, how group settings apply, and where to set tags for an RDS DB instance. Its cheapskate resource type name is rds-instance.

## Eligibility

Eligible instances do not belong to a DB cluster and do not use RDS Custom. Do not manage Aurora member instances individually. The AWS configuration must support starting and stopping.

For Aurora, see [Aurora DB cluster configuration](rds-cluster.md).

## DynamoDB and CLI

There are no DynamoDB attributes specific to this resource type. The membership group's schedule and override control startup and shutdown of the DB instance.

For applicable attributes, see [Group configuration DynamoDB format](../group_config.md#dynamodb-format). For registration and update examples, see [CLI configuration examples](../cli_records.md).

## Resource tags

Set cheapskate:group on the DB instance itself.

To assign dev-db to group dev:

```sh
aws rds add-tags-to-resource --region ap-northeast-1 \
  --resource-name arn:aws:rds:ap-northeast-1:123456789012:db:dev-db \
  --tags Key=cheapskate:group,Value=dev
```

For connection and group assumptions, see [CLI example prerequisites](../cli_records.md#prerequisites). For tagging arguments, see [AWS CLI add-tags-to-resource](https://docs.aws.amazon.com/cli/latest/reference/rds/add-tags-to-resource.html).

## Applied behavior

The RDS DB instance status available means running and stopped means stopped. Other states defer operations until a later cycle. StartDBInstance and StopDBInstance operate on the individual DB instance.

For inspecting configuration and current state, see [Daily operations](../operations.md#inspecting-configuration-and-current-state).
