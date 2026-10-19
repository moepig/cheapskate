# IAM permissions

This page describes the IAM permissions for the cheapskate reconciler, CLI, and web console. For the AWS resources used by the deployment, see [AWS resources and IAM](aws_resources.md#deployment-infrastructure).

## Policy prerequisites

The examples are identity policies for roles that manage existing resources in one account and Region. Use deployment permissions to provision resources, publish images, and assign tags.

Replace PARTITION, REGION, and ACCOUNT with deployment values. The standard AWS partition is aws.

TABLE is STATE_TABLE_NAME; GROUP is an allowed membership tag value.

DB_INSTANCE, DB_CLUSTER, CLUSTER, SERVICE, and INSTANCE_ID are actual resource identifiers. SCALABLE_TARGET_ID is the suffix of an existing scalable target ARN.

Replace FUNCTION_NAME and TOPIC with the Lambda function name and notification topic name.

Enumerate the target ARNs in the IAM policy's Resource, using an array for multiple resources. Enumerate allowed group names in the membership tag's StringEquals condition. Statements with a membership tag condition stop allowing access when the tag is removed or changed to an unlisted group, even if the ARN still matches.

Use separate statements when different groups must be restricted to different ARNs.

Combine permissions by role as follows:

| Permission | Role | When required |
| --- | --- | --- |
| [Shared resource inspection](#shared-resource-inspection) | Reconciler and web console | Always |
| [Shared resource inspection](#shared-resource-inspection) | CLI | When show displays resource state |
| [Configuration access](#cli-and-web-console-permissions) | CLI and web console | Always |
| [Configuration reads and resource updates](#reconciler-permissions) | Reconciler | Always |
| [Log delivery](#lambda-logging) | Each Lambda | Grant for its own function |
| [SNS notifications](#sns-notifications) | Reconciler | When notifications are enabled |
| [Encryption](#optional-notification-and-encryption-permissions) | Reconciler | When a feature uses encryption |

Each complete JSON policy can be attached separately. Add examples containing a single statement to the appropriate role policy's Statement array.

Remove actions and statements for resource types that are not managed. ECS inspection requires DescribeScalableTargets even when a service has no scalable target. RegisterScalableTarget is needed only to update existing targets.

These Allow policies do not revoke permissions granted by other policies. Remove broader Allow statements attached to the same role to maintain least privilege. Keep DynamoDB group configuration, membership tags, and permitted ARNs aligned. IAM denial does not automatically filter resources out of discovery results.

## Resources and conditions by action

The table maps runtime actions to the restrictions used here. Resource shows the resource portion of an ARN. An asterisk indicates that this configuration does not specify a resource-level restriction.

| Service | IAM action | Resource portion of the ARN | Condition or restriction |
| --- | --- | --- | --- |
| DynamoDB | [dynamodb:Query](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-Query) | table/TABLE | dynamodb:LeadingKeys=CONFIG |
| DynamoDB | [dynamodb:GetItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-GetItem) | table/TABLE | dynamodb:LeadingKeys=CONFIG |
| DynamoDB | [dynamodb:PutItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-PutItem) | table/TABLE | dynamodb:LeadingKeys=CONFIG |
| DynamoDB | [dynamodb:UpdateItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-UpdateItem) | table/TABLE | dynamodb:LeadingKeys=CONFIG |
| DynamoDB | [dynamodb:DeleteItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-DeleteItem) | table/TABLE | dynamodb:LeadingKeys=CONFIG |
| Resource Groups Tagging API | [tag:GetResources](https://docs.aws.amazon.com/service-authorization/latest/reference/list_resourcegroupstaggingapi.html#list_resourcegroupstaggingapi-action-GetResources) | * | aws:RequestedRegion |
| RDS DB instance | [rds:DescribeDBInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-DescribeDBInstances) | db:DB_INSTANCE | aws:ResourceTag/cheapskate:group |
| RDS DB instance | [rds:StartDBInstance](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StartDBInstance) | db:DB_INSTANCE | aws:ResourceTag/cheapskate:group |
| RDS DB instance | [rds:StopDBInstance](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StopDBInstance) | db:DB_INSTANCE | aws:ResourceTag/cheapskate:group |
| RDS DB cluster inspection | [rds:DescribeDBClusters](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-DescribeDBClusters) | * | aws:RequestedRegion |
| RDS DB cluster operations | [rds:StartDBCluster](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StartDBCluster) | cluster:DB_CLUSTER | aws:ResourceTag/cheapskate:group |
| RDS DB cluster operations | [rds:StopDBCluster](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StopDBCluster) | cluster:DB_CLUSTER | aws:ResourceTag/cheapskate:group |
| ECS | [ecs:DescribeServices](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ecs.html#list_ecs-action-DescribeServices) | service/CLUSTER/SERVICE | aws:ResourceTag/cheapskate:group |
| ECS | [ecs:UpdateService](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ecs.html#list_ecs-action-UpdateService) | service/CLUSTER/SERVICE | aws:ResourceTag/cheapskate:group |
| Application Auto Scaling | [application-autoscaling:DescribeScalableTargets](https://docs.aws.amazon.com/service-authorization/latest/reference/list_application-autoscaling.html#list_application-autoscaling-action-DescribeScalableTargets) | * | aws:RequestedRegion |
| Application Auto Scaling | [application-autoscaling:RegisterScalableTarget](https://docs.aws.amazon.com/service-authorization/latest/reference/list_application-autoscaling.html#list_application-autoscaling-action-RegisterScalableTarget) | scalable-target/SCALABLE_TARGET_ID | Exact scalable target ARN |
| EC2 | [ec2:DescribeInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-DescribeInstances) | * | aws:RequestedRegion |
| EC2 | [ec2:StartInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-StartInstances) | instance/INSTANCE_ID | aws:ResourceTag/cheapskate:group |
| EC2 | [ec2:StopInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-StopInstances) | instance/INSTANCE_ID | aws:ResourceTag/cheapskate:group |
| SNS | [sns:Publish](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sns.html#list_sns-action-Publish) | TOPIC | Exact topic ARN |
| CloudWatch Logs | [logs:CreateLogStream](https://docs.aws.amazon.com/service-authorization/latest/reference/list_logs.html#list_logs-action-CreateLogStream) | log-group:/aws/lambda/FUNCTION_NAME:log-stream:* | Exact log group in Resource |
| CloudWatch Logs | [logs:PutLogEvents](https://docs.aws.amazon.com/service-authorization/latest/reference/list_logs.html#list_logs-action-PutLogEvents) | log-group:/aws/lambda/FUNCTION_NAME:log-stream:* | Exact log group in Resource |

rds:DescribeDBClusters uses queries without a cluster name to resolve clusters from resource IDs.

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
        "rds:DescribeDBClusters",
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
      "Sid": "DescribeManagedEcsServices",
      "Effect": "Allow",
      "Action": "ecs:DescribeServices",
      "Resource": "arn:PARTITION:ecs:REGION:ACCOUNT:service/CLUSTER/SERVICE",
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

tag:GetResources cannot be restricted by resource ARN or resource tags. The TagFilters and ResourceTypeFilters sent by cheapskate are query filters, not IAM restrictions. ec2:DescribeInstances also cannot be restricted by instance ARN or tags. application-autoscaling:DescribeScalableTargets does not support the namespace or dimension condition keys either.

For these three actions and rds:DescribeDBClusters, aws:RequestedRegion restricts the API endpoint Region. They can still expose information about unmanaged resources in that Region. See [aws:RequestedRegion](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition-keys.html#condition-keys-requestedregion).

RDS DB instance Describe requests specify a DB identifier in this deployment, allowing restriction by ARN and membership tag. RDS DB cluster discovery first queries by ARN, then falls back to the cluster name and the [DescribeDBClusters db-cluster-resource-id filter](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_DescribeDBClusters.html) when a candidate is not found. The resource ID query does not specify a cluster name, so the inspection policy uses Resource * to allow it. Cluster start/stop permissions remain restricted by target ARN and membership tag. ECS inspection and updates identify the cluster and service through the long service ARN and require a matching membership tag on the service itself.

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
      "Resource": "arn:PARTITION:application-autoscaling:REGION:ACCOUNT:scalable-target/SCALABLE_TARGET_ID"
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

dynamodb:LeadingKeys is multivalued, so ForAllValues:StringEquals limits access to the CONFIG partition. The Query, GetItem, PutItem, UpdateItem, and DeleteItem operations allowed here all require a partition key. See [DynamoDB condition keys](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/specifying-conditions.html) and [Query key conditions](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Query.html).

LeadingKeys restricts pk, not sk=GROUP#.... All groups share the CONFIG partition, so this condition cannot isolate individual groups.

### RDS and EC2

Use separate statements for RDS DB instances and clusters. Stopping a DB instance does not request a snapshot, so no snapshot ARN or snapshot creation permission is granted. See the supported resources for [StopDBInstance](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StopDBInstance).

EC2 start and stop permissions require both an enumerated instance ARN and its membership tag. If startup authorization also involves a License Manager license configuration or a Dedicated Host resource group, allow StartInstances on only those additional ARNs in a separate statement. Do not copy the instance tag condition to that statement. See [StartInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-StartInstances) and the [License Manager permissions example](https://docs.aws.amazon.com/solutions/latest/instance-scheduler-on-aws/security-1.html#ec2-license-manager).

### ECS and Application Auto Scaling

The IAM policy Resource for RegisterScalableTarget is the ScalableTargetARN returned by Application Auto Scaling, rather than the ECS service ARN. Do not append the API ResourceId value service/CLUSTER/SERVICE to scalable-target/. Retrieve the existing ARN during deployment:

```console
aws application-autoscaling describe-scalable-targets \
  --region REGION \
  --service-namespace ecs \
  --scalable-dimension ecs:service:DesiredCount \
  --resource-ids service/CLUSTER/SERVICE \
  --query 'ScalableTargets[].ScalableTargetARN' \
  --output text
```

A scalable target is uniquely identified by ResourceId, ServiceNamespace, and ScalableDimension. Setting Resource to the ARN returned by this command restricts updates to the scalable target for that service's desired count. Update the policy if recreating a scalable target changes its ARN.

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

Resource must match NOTIFICATION_TOPIC_ARN. Omit this statement when notifications are disabled.

### SNS encryption

Publishing to an encrypted topic requires [kms:GenerateDataKey](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html#list_kms-action-GenerateDataKey) and [kms:Decrypt](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html#list_kms-action-Decrypt) on its KMS key. Set SNS_KEY_ID to the topic's KMS key ID. Restrict access to SNS and the topic's encryption context:

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
      "kms:EncryptionContext:aws:sns:topicArn": "arn:PARTITION:sns:REGION:ACCOUNT:TOPIC"
    }
  }
}
```

For permissions and encryption context, see [SNS key management](https://docs.aws.amazon.com/sns/latest/dg/sns-key-management.html) and [SNS encryption configuration](https://docs.aws.amazon.com/sns/latest/dg/sns-enable-encryption-for-topic.html). For kms:ViaService names, see [KMS condition keys](https://docs.aws.amazon.com/kms/latest/developerguide/conditions-kms.html#conditions-kms-via-service).

### Starting instances with encrypted EBS volumes

Starting EC2 instances with encrypted EBS volumes also requires [kms:CreateGrant](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html#list_kms-action-CreateGrant) on the KMS keys in use. Set EBS_KEY_ID to the volume's KMS key ID and EBS_VOLUME_ID to its volume ID, including the vol- prefix. This statement permits EBS to create grants for decrypting the specified volumes:

```json
{
  "Sid": "CreateEbsStartGrant",
  "Effect": "Allow",
  "Action": "kms:CreateGrant",
  "Resource": "arn:PARTITION:kms:REGION:ACCOUNT:key/EBS_KEY_ID",
  "Condition": {
    "StringEquals": {
      "kms:ViaService": "ec2.REGION.amazonaws.com",
      "kms:EncryptionContext:aws:ebs:id": [
        "EBS_VOLUME_ID"
      ]
    },
    "ForAllValues:StringEquals": {
      "kms:GrantOperations": [
        "Decrypt"
      ]
    }
  }
}
```

The kms:ViaService value ec2.REGION.amazonaws.com identifies KMS calls through EBS. kms:GrantOperations allows only Decrypt, and kms:EncryptionContext:aws:ebs:id restricts the allowed volume IDs. This statement does not allow grants for other volumes encrypted with the same key. For multiple volumes, use a separate statement for each key and enumerate the managed volume IDs that use it. Update this list when adding or replacing volumes.

CreateGrant requires Operations and rejects an empty operation list. See [kms:GrantOperations](https://docs.aws.amazon.com/kms/latest/developerguide/conditions-kms.html#conditions-kms-grant-operations), [EBS encryption context](https://docs.aws.amazon.com/kms/latest/developerguide/services-ebs.html), and the [EBS grant creation example](https://docs.aws.amazon.com/kms/latest/developerguide/ct-ec2two.html).

## Execution-role trust and periodic invocation

Configure role trust and Lambda invocation separately from resource operation permissions. The following sections describe uses of [sts:AssumeRole](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sts.html#list_sts-action-AssumeRole) and [lambda:InvokeFunction](https://docs.aws.amazon.com/service-authorization/latest/reference/list_lambda.html#list_lambda-action-InvokeFunction).

### Lambda execution roles

Use a dedicated execution role for each reconciler and web Lambda function. Its trust policy allows sts:AssumeRole for Principal.Service=lambda.amazonaws.com.

### EventBridge Scheduler invocation

The invocation role allows lambda:InvokeFunction. Restrict Resource to the exact ARN of the reconciler function or alias being invoked. Its trust policy allows sts:AssumeRole for Principal.Service=scheduler.amazonaws.com.

Set ArnEquals aws:SourceArn to arn:PARTITION:scheduler:REGION:ACCOUNT:schedule-group/SCHEDULE_GROUP.

### EventBridge rule invocation

The Lambda resource policy allows lambda:InvokeFunction for Principal.Service=events.amazonaws.com. Restrict it to the exact function ARN.

Set ArnLike aws:SourceArn to arn:PARTITION:events:REGION:ACCOUNT:rule/RULE_NAME.

Set aws:SourceArn to a complete ARN including the account ID. This also restricts the source account. See [aws:SourceArn](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition-keys.html#condition-keys-sourcearn).

For Scheduler, aws:SourceArn must be the schedule group ARN, rather than an individual schedule ARN. See [Scheduler trust policy conditions](https://docs.aws.amazon.com/scheduler/latest/UserGuide/cross-service-confused-deputy-prevention.html). For EventBridge rules and Lambda execution-role setup, see [EventBridge Lambda invocation permissions](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-use-resource-based.html#eb-lambda-permissions) and [Lambda execution roles](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html).
