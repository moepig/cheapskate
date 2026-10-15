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

## Policy prerequisites

The examples manage existing resources in one account and Region. The actions, resource types, and condition keys were checked against the AWS Service Authorization Reference on 2026-10-15. Use deployment permissions to provision resources, publish images, and assign tags.

Replace PARTITION, REGION, and ACCOUNT with deployment values. The standard AWS partition is aws. TABLE is STATE_TABLE_NAME; GROUP is an allowed membership tag value. DB_INSTANCE, DB_CLUSTER, CLUSTER, SERVICE, and INSTANCE_ID are actual resource identifiers. SCALABLE_TARGET_ID is the suffix of an existing scalable target ARN. Replace FUNCTION_NAME and TOPIC with the Lambda function name and notification topic name.

Enumerate the target ARNs in Resource, using an array for multiple resources. Enumerate allowed group names in the membership tag's StringEquals condition. Statements with a membership tag condition require both the ARN and tag value to match. Use separate statements when different groups must be restricted to different ARNs.

Combine permissions by role as follows. Each complete JSON policy can be attached separately. Add examples containing a single statement to the appropriate role policy's Statement array.

| Role | Permissions |
| --- | --- |
| Reconciler | Shared resource inspection, reconciler permissions, Lambda logging, and notification/encryption permissions used by the deployment |
| CLI | CLI and web-console permissions; add shared resource inspection when using show |
| Web console | CLI and web-console permissions and shared resource inspection; add its own logging permissions when hosted on Lambda |

Remove actions and statements for resource types that are not managed. ECS inspection requires DescribeScalableTargets even when a service has no scalable target. RegisterScalableTarget is needed only to update existing targets.

These Allow policies do not revoke permissions granted by other policies. Remove broader Allow statements attached to the same role to maintain least privilege. Keep DynamoDB group configuration, membership tags, and permitted ARNs aligned. IAM denial does not automatically filter resources out of discovery results.

## Resources and conditions by action

The following table maps runtime actions to restrictions used in this deployment. The Resource column shows the resource portion of an ARN. Rows containing “*” do not support resource-level permissions.

| Service | IAM actions and official references | Resource portion of the ARN | Condition or restriction used here |
| --- | --- | --- | --- |
| DynamoDB | [dynamodb:Query](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-Query), [dynamodb:GetItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-GetItem), [dynamodb:PutItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-PutItem), [dynamodb:UpdateItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-UpdateItem), [dynamodb:DeleteItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-DeleteItem) | table/TABLE | dynamodb:LeadingKeys=CONFIG, Null=false |
| Resource Groups Tagging API | [tag:GetResources](https://docs.aws.amazon.com/service-authorization/latest/reference/list_resourcegroupstaggingapi.html#list_resourcegroupstaggingapi-action-GetResources) | * | aws:RequestedRegion |
| RDS DB instance | [rds:DescribeDBInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-DescribeDBInstances), [rds:StartDBInstance](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StartDBInstance), [rds:StopDBInstance](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StopDBInstance) | db:DB_INSTANCE | aws:ResourceTag/cheapskate:group |
| RDS DB cluster | [rds:DescribeDBClusters](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-DescribeDBClusters), [rds:StartDBCluster](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StartDBCluster), [rds:StopDBCluster](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StopDBCluster) | cluster:DB_CLUSTER | aws:ResourceTag/cheapskate:group |
| ECS | [ecs:DescribeServices](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ecs.html#list_ecs-action-DescribeServices), [ecs:UpdateService](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ecs.html#list_ecs-action-UpdateService) | service/CLUSTER/SERVICE | aws:ResourceTag/cheapskate:group, ecs:cluster |
| Application Auto Scaling | [application-autoscaling:DescribeScalableTargets](https://docs.aws.amazon.com/service-authorization/latest/reference/list_application-autoscaling.html#list_application-autoscaling-action-DescribeScalableTargets) | * | aws:RequestedRegion |
| Application Auto Scaling | [application-autoscaling:RegisterScalableTarget](https://docs.aws.amazon.com/service-authorization/latest/reference/list_application-autoscaling.html#list_application-autoscaling-action-RegisterScalableTarget) | scalable-target/SCALABLE_TARGET_ID | application-autoscaling:service-namespace, application-autoscaling:scalable-dimension |
| EC2 | [ec2:DescribeInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-DescribeInstances) | * | aws:RequestedRegion |
| EC2 | [ec2:StartInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-StartInstances), [ec2:StopInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-StopInstances) | instance/INSTANCE_ID | aws:ResourceTag/cheapskate:group |
| SNS | [sns:Publish](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sns.html#list_sns-action-Publish) | TOPIC | Exact topic ARN |
| CloudWatch Logs | [logs:CreateLogStream](https://docs.aws.amazon.com/service-authorization/latest/reference/list_logs.html#list_logs-action-CreateLogStream), [logs:PutLogEvents](https://docs.aws.amazon.com/service-authorization/latest/reference/list_logs.html#list_logs-action-PutLogEvents) | log-group:/aws/lambda/FUNCTION_NAME:log-stream:* | Exact log group in Resource |

## Shared resource inspection

Attach this inspection policy to the reconciler, CLI credentials used for show, and the web console for group detail:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReadRegionalInventory",
      "Effect": "Allow",
      "Action": [
        "tag:GetResources",
        "ec2:DescribeInstances",
        "application-autoscaling:DescribeScalableTargets"
      ],
      "Resource": "*",
      "Condition": {
        "StringEquals": {
          "aws:RequestedRegion": "REGION"
        }
      }
    },
    {
      "Sid": "DescribeManagedDbInstances",
      "Effect": "Allow",
      "Action": "rds:DescribeDBInstances",
      "Resource": "arn:PARTITION:rds:REGION:ACCOUNT:db:DB_INSTANCE",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    },
    {
      "Sid": "DescribeManagedDbClusters",
      "Effect": "Allow",
      "Action": "rds:DescribeDBClusters",
      "Resource": "arn:PARTITION:rds:REGION:ACCOUNT:cluster:DB_CLUSTER",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    },
    {
      "Sid": "DescribeManagedEcsServices",
      "Effect": "Allow",
      "Action": "ecs:DescribeServices",
      "Resource": "arn:PARTITION:ecs:REGION:ACCOUNT:service/CLUSTER/SERVICE",
      "Condition": {
        "ArnEquals": {
          "ecs:cluster": "arn:PARTITION:ecs:REGION:ACCOUNT:cluster/CLUSTER"
        },
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    }
  ]
}
```

tag:GetResources cannot be restricted by resource ARN or resource tags. The TagFilters and ResourceTypeFilters sent by cheapskate are query filters, not IAM restrictions. ec2:DescribeInstances also cannot be restricted by instance ARN or tags. application-autoscaling:DescribeScalableTargets does not support the namespace or dimension condition keys; do not copy the update operation's conditions onto this action.

For these three actions, aws:RequestedRegion restricts the API endpoint Region. They can still expose information about unmanaged resources in that Region. This key generally limits the endpoint called, rather than the scope of an operation's effects. See [aws:RequestedRegion](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition-keys.html#condition-keys-requestedregion).

RDS Describe requests specify a DB identifier in this deployment, allowing restriction by ARN and membership tag. ECS inspection and updates use the long service ARN and the service's own tags; ecs:cluster takes the cluster ARN. Cluster tags do not substitute for service tags.

## Reconciler permissions

Add this policy for configuration reads and resource updates alongside shared resource inspection:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReadGroupConfiguration",
      "Effect": "Allow",
      "Action": "dynamodb:Query",
      "Resource": "arn:PARTITION:dynamodb:REGION:ACCOUNT:table/TABLE",
      "Condition": {
        "ForAllValues:StringEquals": {
          "dynamodb:LeadingKeys": [
            "CONFIG"
          ]
        },
        "Null": {
          "dynamodb:LeadingKeys": "false"
        }
      }
    },
    {
      "Sid": "ControlManagedDbInstances",
      "Effect": "Allow",
      "Action": [
        "rds:StartDBInstance",
        "rds:StopDBInstance"
      ],
      "Resource": "arn:PARTITION:rds:REGION:ACCOUNT:db:DB_INSTANCE",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    },
    {
      "Sid": "ControlManagedDbClusters",
      "Effect": "Allow",
      "Action": [
        "rds:StartDBCluster",
        "rds:StopDBCluster"
      ],
      "Resource": "arn:PARTITION:rds:REGION:ACCOUNT:cluster:DB_CLUSTER",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    },
    {
      "Sid": "UpdateManagedEcsServices",
      "Effect": "Allow",
      "Action": "ecs:UpdateService",
      "Resource": "arn:PARTITION:ecs:REGION:ACCOUNT:service/CLUSTER/SERVICE",
      "Condition": {
        "ArnEquals": {
          "ecs:cluster": "arn:PARTITION:ecs:REGION:ACCOUNT:cluster/CLUSTER"
        },
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    },
    {
      "Sid": "UpdateManagedScalableTargets",
      "Effect": "Allow",
      "Action": "application-autoscaling:RegisterScalableTarget",
      "Resource": "arn:PARTITION:application-autoscaling:REGION:ACCOUNT:scalable-target/SCALABLE_TARGET_ID",
      "Condition": {
        "StringEquals": {
          "application-autoscaling:service-namespace": "ecs",
          "application-autoscaling:scalable-dimension": "ecs:service:DesiredCount"
        }
      }
    },
    {
      "Sid": "ControlManagedEc2Instances",
      "Effect": "Allow",
      "Action": [
        "ec2:StartInstances",
        "ec2:StopInstances"
      ],
      "Resource": "arn:PARTITION:ec2:REGION:ACCOUNT:instance/INSTANCE_ID",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    }
  ]
}
```

### DynamoDB

The reconciler queries only the CONFIG partition of the table. It needs no index ARN, Scan permission, or write permission.

dynamodb:LeadingKeys is multivalued and requires ForAllValues:StringEquals. Null=false prevents ForAllValues from allowing a request when the key is absent. See [DynamoDB condition keys](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/specifying-conditions.html) and [IAM set operators](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_condition_operators.html#Conditions_String).

LeadingKeys restricts pk, not sk=GROUP#.... All groups share the CONFIG partition, so this condition cannot isolate individual groups. Query and GetItem read all attributes; requiring dynamodb:Select=SPECIFIC_ATTRIBUTES would be incompatible with current reads.

### RDS and EC2

Use separate statements for RDS DB instances and clusters. Stopping a DB instance does not request a snapshot, so no snapshot ARN or snapshot creation permission is granted. See the supported resources for [StopDBInstance](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StopDBInstance).

EC2 start and stop permissions require both an enumerated instance ARN and its membership tag. If startup authorization also involves a License Manager license configuration or a Dedicated Host resource group, allow StartInstances on only those additional ARNs in a separate statement. Do not copy the instance tag condition to that statement. See [StartInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-StartInstances) and the [License Manager permissions example](https://docs.aws.amazon.com/solutions/latest/instance-scheduler-on-aws/security-1.html#ec2-license-manager).

### ECS and Application Auto Scaling

RegisterScalableTarget authorizes the ScalableTargetARN returned by Application Auto Scaling, rather than the ECS service ARN. Do not append the API ResourceId value service/CLUSTER/SERVICE to scalable-target/. Retrieve the existing ARN during deployment:

```console
aws application-autoscaling describe-scalable-targets \
  --region REGION \
  --service-namespace ecs \
  --scalable-dimension ecs:service:DesiredCount \
  --resource-ids service/CLUSTER/SERVICE \
  --query 'ScalableTargets[].ScalableTargetARN' \
  --output text
```

Require service-namespace=ecs and scalable-dimension=ecs:service:DesiredCount for updates. Update the policy if recreating a scalable target changes its ARN. For an additional tag restriction, tag the scalable target itself with cheapskate:group, then add allowed aws:ResourceTag/cheapskate:group values to the same StringEquals condition. The ECS service's tag alone does not satisfy that condition. See [Application Auto Scaling tag access control](https://docs.aws.amazon.com/autoscaling/application/userguide/resource-tagging-support.html).

Provision scalable targets and AWSServiceRoleForApplicationAutoScaling_ECSService during deployment. The reconciler updates existing targets without sending RoleARN or Tags, so this use does not need iam:PassRole, iam:CreateServiceLinkedRole, or application-autoscaling:TagResource. See [RegisterScalableTarget parameters](https://docs.aws.amazon.com/autoscaling/application/APIReference/API_RegisterScalableTarget.html) and [Application Auto Scaling service-linked roles](https://docs.aws.amazon.com/autoscaling/application/userguide/application-auto-scaling-service-linked-roles.html).

ecs:UpdateService has no condition key that limits the request to changing only desiredCount. RegisterScalableTarget has no condition keys for constraining MinCapacity, MaxCapacity, or SuspendedState values. Restricting target ARNs and tags does not reproduce the application's validation of these values in IAM.

## CLI and web-console permissions

Use this policy for configuration reads and writes. Add shared resource inspection when displaying live resource state:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ManageGroupConfiguration",
      "Effect": "Allow",
      "Action": [
        "dynamodb:Query",
        "dynamodb:GetItem",
        "dynamodb:PutItem",
        "dynamodb:UpdateItem",
        "dynamodb:DeleteItem"
      ],
      "Resource": "arn:PARTITION:dynamodb:REGION:ACCOUNT:table/TABLE",
      "Condition": {
        "ForAllValues:StringEquals": {
          "dynamodb:LeadingKeys": [
            "CONFIG"
          ]
        },
        "Null": {
          "dynamodb:LeadingKeys": "false"
        }
      }
    }
  ]
}
```

For inspection only, remove PutItem, UpdateItem, and DeleteItem. This also applies when Terraform owns configuration and the CLI is used only to inspect it. Do not grant administration interfaces the reconciler's resource update or SNS notification permissions.

Changing DynamoDB configuration changes start/stop requests in subsequent reconciler cycles. Treat configuration write access as authority to change the managed resources' operating state.

## Lambda logging

Create log groups during deployment. Give each Lambda execution role permission to create and write streams only within its own log group:

```json
{
  "Sid": "WriteFunctionLogs",
  "Effect": "Allow",
  "Action": [
    "logs:CreateLogStream",
    "logs:PutLogEvents"
  ],
  "Resource": "arn:PARTITION:logs:REGION:ACCOUNT:log-group:/aws/lambda/FUNCTION_NAME:log-stream:*"
}
```

Set FUNCTION_NAME to the function associated with each role. Substitute the actual log group name when using a custom group. Stream names are generated at runtime, so only that suffix uses a wildcard. A deployment with precreated log groups does not need logs:CreateLogGroup. See [Sending Lambda logs to CloudWatch Logs](https://docs.aws.amazon.com/lambda/latest/dg/monitoring-cloudwatchlogs.html).

cheapskate emits metrics through EMF in its logs, so cloudwatch:PutMetricData is unnecessary.

## Optional notification and encryption permissions

Add the following statements only for the features being used. KMS access must also be authorized by the relevant key policy.

### SNS notifications

When NOTIFICATION_TOPIC_ARN is set, add this statement to the reconciler role:

```json
{
  "Sid": "PublishActionNotifications",
  "Effect": "Allow",
  "Action": "sns:Publish",
  "Resource": "arn:PARTITION:sns:REGION:ACCOUNT:TOPIC"
}
```

Resource must match NOTIFICATION_TOPIC_ARN. The sns:Endpoint and sns:Protocol condition keys do not apply to Publish. Omit this statement when notifications are disabled.

### SNS encryption

Publishing to an encrypted topic requires [kms:GenerateDataKey](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html#list_kms-action-GenerateDataKey) and [kms:Decrypt](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html#list_kms-action-Decrypt) on its key. Set SNS_KEY_ID to the topic's key ID. Restrict access to SNS and the topic's encryption context:

```json
{
  "Sid": "UseNotificationKey",
  "Effect": "Allow",
  "Action": [
    "kms:GenerateDataKey",
    "kms:Decrypt"
  ],
  "Resource": "arn:PARTITION:kms:REGION:ACCOUNT:key/SNS_KEY_ID",
  "Condition": {
    "StringEquals": {
      "kms:ViaService": "sns.REGION.amazonaws.com",
      "kms:CallerAccount": "ACCOUNT",
      "kms:EncryptionContext:aws:sns:topicArn": "arn:PARTITION:sns:REGION:ACCOUNT:TOPIC"
    }
  }
}
```

For permissions and encryption context, see [SNS key management](https://docs.aws.amazon.com/sns/latest/dg/sns-key-management.html) and [SNS encryption configuration](https://docs.aws.amazon.com/sns/latest/dg/sns-enable-encryption-for-topic.html). For kms:ViaService names, see [KMS condition keys](https://docs.aws.amazon.com/kms/latest/developerguide/conditions-kms.html#conditions-kms-via-service).

### Starting instances with encrypted EBS volumes

Starting EC2 instances with encrypted EBS volumes also requires [kms:CreateGrant](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html#list_kms-action-CreateGrant) on the keys in use. Set EBS_KEY_ID to the volume's key ID. This statement permits grants for decrypting existing volumes:

```json
{
  "Sid": "CreateEbsStartGrant",
  "Effect": "Allow",
  "Action": "kms:CreateGrant",
  "Resource": "arn:PARTITION:kms:REGION:ACCOUNT:key/EBS_KEY_ID",
  "Condition": {
    "StringEquals": {
      "kms:ViaService": "ec2.REGION.amazonaws.com",
      "kms:CallerAccount": "ACCOUNT"
    },
    "Bool": {
      "kms:GrantIsForAWSResource": "true"
    },
    "ForAllValues:StringEquals": {
      "kms:GrantOperations": [
        "Decrypt"
      ],
      "kms:EncryptionContextKeys": [
        "aws:ebs:id"
      ]
    },
    "Null": {
      "kms:GrantOperations": "false",
      "kms:EncryptionContextKeys": "false"
    }
  }
}
```

Only grants created through EC2 for AWS resources are allowed, with Decrypt as the grant operation and aws:ebs:id as the encryption context key. Null=false guards the multivalued conditions. These restrictions follow the [AWS Instance Scheduler example for encrypted EBS volumes](https://docs.aws.amazon.com/solutions/latest/instance-scheduler-on-aws/security-1.html#encrypted-ec2-ebs-volumes).

## Execution-role trust and periodic invocation

Configure role trust and Lambda invocation separately from resource operation permissions. Uses of [sts:AssumeRole](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sts.html#list_sts-action-AssumeRole) and [lambda:InvokeFunction](https://docs.aws.amazon.com/service-authorization/latest/reference/list_lambda.html#list_lambda-action-InvokeFunction) are:

| Policy location | Permission | Restriction |
| --- | --- | --- |
| Reconciler / web Lambda execution-role trust policy | sts:AssumeRole for Principal.Service=lambda.amazonaws.com | Use a dedicated execution role for each function |
| EventBridge Scheduler invocation role | lambda:InvokeFunction | Resource is the exact ARN of the reconciler function or alias being invoked |
| Scheduler invocation-role trust policy | sts:AssumeRole for Principal.Service=scheduler.amazonaws.com | StringEquals aws:SourceAccount=ACCOUNT and ArnEquals aws:SourceArn=arn:PARTITION:scheduler:REGION:ACCOUNT:schedule-group/SCHEDULE_GROUP |
| Lambda resource policy when using an EventBridge rule | lambda:InvokeFunction for Principal.Service=events.amazonaws.com | Exact function ARN; ArnLike aws:SourceArn to the rule ARN without wildcards and StringEquals aws:SourceAccount to ACCOUNT |

For Scheduler, aws:SourceArn must be the schedule group ARN, rather than an individual schedule ARN. See [Scheduler trust policy conditions](https://docs.aws.amazon.com/scheduler/latest/UserGuide/cross-service-confused-deputy-prevention.html). For EventBridge rules and Lambda execution-role setup, see [EventBridge Lambda invocation permissions](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-use-resource-based.html#eb-lambda-permissions) and [Lambda execution roles](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html).
