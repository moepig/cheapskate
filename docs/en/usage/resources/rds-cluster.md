# Aurora DB cluster configuration

This document describes eligibility, how group settings apply, and where to set tags for an Aurora DB cluster. Its cheapskate resource type name is rds-cluster.

## Eligibility

Eligible DB clusters have an engine beginning with aurora. Non-Aurora RDS DB clusters are excluded. The AWS configuration must support starting and stopping. Configuration and operations apply to the cluster, without independent startup or shutdown settings for its member instances.

## DynamoDB and CLI

There are no DynamoDB attributes specific to this resource type. The membership group's schedule and override control startup and shutdown of the DB cluster.

For applicable attributes, see [Group configuration DynamoDB format](../group_config.md#dynamodb-format). For registration and update examples, see [CLI configuration examples](../cli_records.md).

## Resource tags

Set cheapskate:group on the DB cluster itself. Tagging a member DB instance does not configure cluster membership.

Listings and start/stop operations use the cluster name in DBClusterIdentifier. The cluster- resource ID in DbClusterResourceId is a separate identifier. A cluster discovered through both identifiers appears only once in the list.

The ARN in listings is the DBClusterArn returned by RDS. Its final component can differ from the cluster name in DBClusterIdentifier.

To assign dev-cluster to group dev:

```sh
aws rds add-tags-to-resource --region ap-northeast-1 \
  --resource-name arn:aws:rds:ap-northeast-1:123456789012:cluster:dev-cluster \
  --tags Key=cheapskate:group,Value=dev
```

For connection and group assumptions, see [CLI example prerequisites](../cli_records.md#prerequisites). For tagging arguments, see [AWS CLI add-tags-to-resource](https://docs.aws.amazon.com/cli/latest/reference/rds/add-tags-to-resource.html).

## Applied behavior

The Aurora DB cluster status available means running and stopped means stopped. Other states defer operations until a later cycle. StartDBCluster and StopDBCluster operate on the entire cluster.

A member DB instance with its own membership tag produces an unsupported-instance error.

For inspecting configuration and current state, see [Daily operations](../operations.md#inspecting-configuration-and-current-state).
