# AWS resources and IAM

This document lists deployment infrastructure and permissions for the reconciler and administration interfaces.

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

## Reconciler permissions

Grant permissions for the resource types being managed:

| Service | IAM actions |
| --- | --- |
| DynamoDB | dynamodb:Query |
| Resource Groups Tagging API | tag:GetResources |
| RDS | rds:DescribeDBInstances, rds:StartDBInstance, rds:StopDBInstance, rds:DescribeDBClusters, rds:StartDBCluster, rds:StopDBCluster |
| ECS | ecs:DescribeServices, ecs:UpdateService |
| Application Auto Scaling | application-autoscaling:DescribeScalableTargets, application-autoscaling:RegisterScalableTarget |
| EC2 | ec2:DescribeInstances, ec2:StartInstances, ec2:StopInstances |
| SNS | sns:Publish to the configured topic |
| CloudWatch Logs | Lambda log delivery |

DynamoDB access is read-only. This example restricts reads to the CONFIG partition. Replace REGION, ACCOUNT, and TABLE for the deployment.

```json
{
  "Effect": "Allow",
  "Action": "dynamodb:Query",
  "Resource": "arn:aws:dynamodb:REGION:ACCOUNT:table/TABLE",
  "Condition": {
    "ForAllValues:StringEquals": {
      "dynamodb:LeadingKeys": ["CONFIG"]
    }
  }
}
```

## CLI and web-console permissions

For group configuration reads and writes, use this policy example:

```json
{
  "Effect": "Allow",
  "Action": [
    "dynamodb:Query",
    "dynamodb:GetItem",
    "dynamodb:PutItem",
    "dynamodb:UpdateItem",
    "dynamodb:DeleteItem"
  ],
  "Resource": "arn:aws:dynamodb:REGION:ACCOUNT:table/TABLE",
  "Condition": {
    "ForAllValues:StringEquals": {
      "dynamodb:LeadingKeys": ["CONFIG"]
    }
  }
}
```

Show and group detail also require tag:GetResources and the supported services' read-only Describe operations. Administration interfaces do not start or stop AWS resources.
