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

## IAM permissions

Choose the permission scope for its configuration and policy examples.

| Permission scope | Content |
| --- | --- |
| <a id="policy-prerequisites"></a>[Policy prerequisites](iam.md#policy-prerequisites) | Scope and placeholder values |
| <a id="resources-and-conditions-by-action"></a>[Resources and conditions by action](iam.md#resources-and-conditions-by-action) | Actions, Resource, and Condition |
| <a id="shared-resource-inspection"></a>[Shared resource inspection](iam.md#shared-resource-inspection) | Inspection for the reconciler and administration interfaces |
| <a id="reconciler-permissions"></a>[Reconciler permissions](iam.md#reconciler-permissions) | Resource start and stop permissions |
| <a id="dynamodb"></a>[DynamoDB](iam.md#dynamodb) | Reconciler Query access to the CONFIG partition |
| <a id="rds-and-ec2"></a>[RDS and EC2](iam.md#rds-and-ec2) | Reconciler ARN and membership-tag conditions |
| <a id="ecs-and-application-auto-scaling"></a>[ECS and Application Auto Scaling](iam.md#ecs-and-application-auto-scaling) | Existing scalable target updates and ARN lookup |
| <a id="cli-and-web-console-permissions"></a>[CLI and web-console permissions](iam.md#cli-and-web-console-permissions) | Group configuration reads and writes |
| <a id="lambda-logging"></a>[Lambda logging](iam.md#lambda-logging) | Log stream creation and delivery |
| <a id="optional-notification-and-encryption-permissions"></a>[Optional notification and encryption permissions](iam.md#optional-notification-and-encryption-permissions) | Additional permissions for notifications and encryption |
| <a id="sns-notifications"></a>[SNS notifications](iam.md#sns-notifications) | Publishing to the SNS topic |
| <a id="sns-encryption"></a>[SNS encryption](iam.md#sns-encryption) | KMS permissions for encrypted topics |
| <a id="starting-instances-with-encrypted-ebs-volumes"></a>[Starting instances with encrypted EBS volumes](iam.md#starting-instances-with-encrypted-ebs-volumes) | KMS grants for EBS volumes |
| <a id="execution-role-trust-and-periodic-invocation"></a>[Execution-role trust and periodic invocation](iam.md#execution-role-trust-and-periodic-invocation) | Role trust and periodic invocation permissions |
