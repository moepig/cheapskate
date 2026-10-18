# Setup

This document takes deployment from DynamoDB and Lambda preparation through registering the first group and enabling periodic control.

## Prerequisites

Prepare a reconciler Lambda, DynamoDB table, five-minute trigger, and IAM access to the managed resources. Administration uses the CLI or optional web console. Create AWS infrastructure through your IaC or AWS management tools.

The reconciler and web console use separate container images. For building and publishing to ECR, see [Build](../development/build.md). For infrastructure, see [Deployment infrastructure](aws_resources.md#deployment-infrastructure). For execution-role permissions, see [IAM permissions](iam.md).

## 1. Create the DynamoDB table

Use String partition key pk and String sort key sk, without TTL or secondary indexes. An on-demand example is:

```console
aws dynamodb create-table \
  --table-name cheapskate \
  --billing-mode PAY_PER_REQUEST \
  --attribute-definitions AttributeName=pk,AttributeType=S AttributeName=sk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE
```

## 2. Deploy the reconciler

Precreate the Lambda log group and grant the execution role permissions for the managed resources and enabled features. If using ECS Auto Scaling, also provision the scalable targets and service-linked role first. For policy examples and scalable target ARN lookup, see [IAM permissions](iam.md).

Deploy the reconciler image to Lambda. Match the image and Lambda architectures. Set STATE_TABLE_NAME to the table and DEFAULT_TIMEZONE to the schedule's time zone.

Choose a timeout that completes a full cycle at the maximum expected resource count. Each invocation processes all groups and tagged resources without checkpoints or partitioned execution. Lambda permits a maximum timeout of 900 seconds.

Create one EventBridge Scheduler or EventBridge rule invoking Lambda every five minutes. Keep the trigger disabled until verification. No per-group triggers are needed, and any JSON payload is acceptable.

## 3. Prepare administration

Install cheapskate-cli in a trusted environment with AWS credentials and region configured. For example:

```sh
export AWS_REGION=ap-northeast-1
export CHEAPSKATE_TABLE=cheapskate
export DEFAULT_TIMEZONE=Asia/Tokyo
```

Use the same DEFAULT_TIMEZONE in the CLI, reconciler, and web console. For web authentication and public hosts, see [Web console](web_console.md#publishing-and-access-settings). For other variables, see [Runtime configuration](config.md).

## 4. Register group configuration

Choose either CLI/web-console management or Terraform for each group. For CLI-managed weekday operation from 09:00 to 18:00, register:

```console
cheapskate-cli schedule --group dev -start '0 9 * * 1-5' -stop '0 18 * * 1-5'
```

For ECS, choose startup count and normal bounds and register ecs_max_count at least as large as the tag values. See [Registering an ECS capacity limit](operations.md#registering-an-ecs-capacity-limit).

For Terraform-managed groups, see [Terraform group configuration](terraform_records.md). Do not modify the same group through both Terraform and the CLI or web console.

## 5. Tag resources

Set cheapskate:group=dev on every managed resource. For ECS, set startup count, normal bounds, and bounds mode on the service itself. For value constraints, see [Resource tags](resource_tag.md).

## 6. Verify and enable periodic execution

Invoke the reconciler once manually. Registration during a scheduled running period requests running; registration during a stopped period requests stopped. Inspect results with:

```console
cheapskate-cli -output json show --group dev
```

Check summary logs and current state for every resource. Enable the five-minute trigger once errors are resolved. For observation, see [Logs, metrics, and notifications](monitoring.md). For subsequent changes, see [Daily operations](operations.md).
