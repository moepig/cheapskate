# AWS resources and IAM

This page lists the AWS resources used by a cheapskate deployment. For IAM permissions, see [IAM permissions](iam.md).

## Deployment infrastructure

Provision resources according to their purposes:

| Resource | Purpose |
| --- | --- |
| DynamoDB table | Group configuration |
| Reconciler Lambda and execution role | Periodic control of tagged resources |
| EventBridge Scheduler or rule | Five-minute invocation and permission to invoke Lambda |
| CloudWatch Logs log group | Execution logs |
| ECR repository | Deployment container images |
| Web console and authenticated ingress | Optional browser administration |
| SNS topic and subscriptions | Optional successful-action notifications |
| CloudWatch alarms and dashboards | Optional monitoring |

cheapskate does not provision these resources automatically. For deployment steps, see [Setup](setup.md).

## Policy prerequisites

For IAM permission details, see [Policy prerequisites](iam.md#policy-prerequisites).

## Resources and conditions by action

For IAM permission details, see [Resources and conditions by action](iam.md#resources-and-conditions-by-action).

## Shared resource inspection

For IAM permission details, see [Shared resource inspection](iam.md#shared-resource-inspection).

## Reconciler permissions

For IAM permission details, see [Reconciler permissions](iam.md#reconciler-permissions).

### DynamoDB

For IAM permission details, see [DynamoDB](iam.md#dynamodb).

### RDS and EC2

For IAM permission details, see [RDS and EC2](iam.md#rds-and-ec2).

### ECS and Application Auto Scaling

For IAM permission details, see [ECS and Application Auto Scaling](iam.md#ecs-and-application-auto-scaling).

## CLI and web-console permissions

For IAM permission details, see [CLI and web-console permissions](iam.md#cli-and-web-console-permissions).

## Lambda logging

For IAM permission details, see [Lambda logging](iam.md#lambda-logging).

## Optional notification and encryption permissions

For IAM permission details, see [Optional notification and encryption permissions](iam.md#optional-notification-and-encryption-permissions).

### SNS notifications

For IAM permission details, see [SNS notifications](iam.md#sns-notifications).

### SNS encryption

For IAM permission details, see [SNS encryption](iam.md#sns-encryption).

### Starting instances with encrypted EBS volumes

For IAM permission details, see [Starting instances with encrypted EBS volumes](iam.md#starting-instances-with-encrypted-ebs-volumes).

## Execution-role trust and periodic invocation

For IAM permission details, see [Execution-role trust and periodic invocation](iam.md#execution-role-trust-and-periodic-invocation).
