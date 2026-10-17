# EC2 instance configuration

This document describes eligibility, how group settings apply, and where to set tags for an EC2 instance. Its type name is ec2-instance.

## Eligibility

Eligible EC2 instances do not belong to an Auto Scaling group. The AWS configuration must support starting and stopping.

## DynamoDB and CLI

There are no DynamoDB attributes specific to this resource type. The membership group's schedule and override control startup and shutdown of the EC2 instance. There are no settings to change instance count or instance type.

For applicable attributes, see [Shared DynamoDB settings](README.md#shared-dynamodb-settings). For registration and update commands, see [Shared CLI operations](README.md#shared-cli-operations).

## Resource tags

Set cheapskate:group on the EC2 instance itself. Tags on an EBS volume or launch template do not configure the instance itself. An instance carrying the AWS-assigned aws:autoscaling:groupName tag is excluded even when it has cheapskate:group.

To assign i-0123456789abcdef0 to group dev:

```sh
aws ec2 create-tags --region ap-northeast-1 \
  --resources i-0123456789abcdef0 \
  --tags Key=cheapskate:group,Value=dev
```

For connection and group assumptions, see [Shared prerequisites](README.md#shared-prerequisites). For tagging arguments, see [AWS CLI create-tags](https://docs.aws.amazon.com/cli/latest/reference/ec2/create-tags.html).

## Applied behavior

running means running and stopped means stopped. The pending, stopping, and shutting-down states defer operations until a later cycle. A terminated instance is treated as not found and is not restarted. Operations use StartInstances and StopInstances.

If inspection reports Auto Scaling group membership, remove the instance from cheapskate management.

For inspecting configuration and current state, see [Shared verification](README.md#shared-verification).
