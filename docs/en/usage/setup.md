# Hosting in an AWS account

No IaC template is distributed. This document states the full specification of every AWS resource to create. The tool used (Terraform, CDK, CloudFormation, the Management Console, the CLI) and the naming are free choices. The examples use the AWS CLI, with account `123456789012` and region `ap-northeast-1` as placeholders.

The resources to create are as follows.

| Section | Resource | Required |
|---|---|---|
| §1 | Container images (ECR) | Required |
| §2 | DynamoDB state table | Required |
| §3 | SNS topic and monitoring | Optional |
| §4 | Lambda execution role | Required |
| §5 | Reconciler Lambda function | Required |
| §6 | Periodic reconcile trigger | Required |
| §7 | EventBridge rule (RDS auto-start) | Required |
| §9 | Web console | Optional |

## 1. Placing the container images in ECR

The reconciler and the optional web console are separate images, each carrying one binary. Lambda can pull from ECR only, so whichever route is taken below, the image ends up in ECR.

```console
aws ecr create-repository --repository-name cheapskate-reconciler   # first time only
aws ecr create-repository --repository-name cheapskate-webconsole   # first time only (if §9 is used)
```

### Copying a released image

Every release publishes both images to GHCR for `linux/amd64` and `linux/arm64`. `--platform` picks the single architecture the function runs on, so what lands in ECR is a single-architecture image.

```console
aws ecr get-login-password | docker login --username AWS --password-stdin 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com
docker pull --platform linux/arm64 ghcr.io/moepig/cheapskate-reconciler:v0.1.0
docker tag ghcr.io/moepig/cheapskate-reconciler:v0.1.0 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-reconciler:v0.1.0
docker push 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-reconciler:v0.1.0
```

### Building from source

`make push` goes from building both images to pushing them to ECR.

```console
make push \
  ECR_REPO_RECONCILER=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-reconciler \
  ECR_REPO_WEBCONSOLE=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-webconsole \
  TAG=v0.1.0
```

Without the web console, `make push-reconciler ECR_REPO_RECONCILER=... TAG=v0.1.0` is enough. The Lambda function then refers to the pushed URI, and referring to it by digest is recommended.

The default platform is `linux/arm64`. For x86, use `make push PLATFORM=linux/amd64 ...` and set the Lambda architecture to `x86_64`.

## 2. DynamoDB state table

What the table is required to provide is given below.

| Item | Value |
|---|---|
| Partition key | `pk` (String) |
| Sort key / GSI | none |
| TTL | enabled on the `expires_at` attribute |
| Billing mode | any (on-demand recommended) |

```console
aws dynamodb create-table --table-name cheapskate-state \
  --attribute-definitions AttributeName=pk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST
aws dynamodb update-time-to-live --table-name cheapskate-state \
  --time-to-live-specification "Enabled=true,AttributeName=expires_at"
```

## 3. SNS topic and monitoring (optional)

### SNS topic

Where notifications go when an action is performed and when one fails. The only API the function calls is `sns:Publish`. Omitting the topic and the `NOTIFICATION_TOPIC_ARN` environment variable disables notifications.

```console
aws sns create-topic --name cheapskate-notifications
aws sns subscribe --topic-arn arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications \
  --protocol email --notification-endpoint ops@example.com
```

There are three kinds of notification. The subject is `[cheapskate] <kind>: <group name>/<resource ID>`, and the body is a single JSON object. The kinds and what prompts them are given below.

| Kind | Sent when |
| --- | --- |
| `start` / `stop` | A resource was started or stopped |
| `error` | A failure was recorded. It is not resent while the same error persists |
| `recovered` | A recorded error cleared |

### Metrics

The reconciler emits four metrics every cycle. The namespace is `METRICS_NAMESPACE` (default `cheapskate`), there are no dimensions, and the unit is Count. They are produced through CloudWatch Logs rather than `PutMetricData`, so they need no extra IAM permission. The metrics emitted are given below.

| Metric | Meaning |
| --- | --- |
| `ReconciledResources` | Resources processed in the cycle |
| `ReconcileActions` | Starts and stops performed |
| `ReconcileErrors` | Per-resource and per-group failures |
| `ReconcileAborted` | 1 when the cycle never got going, 0 normally |

These four are billed as custom metrics (a little over a dollar a month in total). If they are not wanted, `METRICS_ENABLED=false` stops them being emitted.

### An alarm for detecting failures

Separately from the notifications, detecting failures calls for an alarm on the Lambda `Errors` metric. For concrete alarms and what each of them catches, see the failure detection section in [troubleshooting.md](troubleshooting.md).

> [!WARNING]
> With neither an SNS topic nor an `Errors` alarm in place, no detection path exists at all, even while every resource keeps failing. Configure at least one of them.

## 4. Lambda execution role

The trust policy is `lambda.amazonaws.com`. The policy to attach is as follows, and no API beyond it is called.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "Logs",
      "Effect": "Allow",
      "Action": [
        "logs:CreateLogGroup",
        "logs:CreateLogStream",
        "logs:PutLogEvents"
      ],
      "Resource": "arn:aws:logs:ap-northeast-1:123456789012:log-group:/aws/lambda/*"
    },
    {
      "Sid": "StateRead",
      "Effect": "Allow",
      "Action": ["dynamodb:Scan", "dynamodb:GetItem"],
      "Resource": "arn:aws:dynamodb:ap-northeast-1:123456789012:table/cheapskate-state"
    },
    {
      "Sid": "StateWriteStatusOnly",
      "Effect": "Allow",
      "Action": ["dynamodb:UpdateItem"],
      "Resource": "arn:aws:dynamodb:ap-northeast-1:123456789012:table/cheapskate-state",
      "Condition": {
        "ForAllValues:StringLike": {"dynamodb:LeadingKeys": ["status#*"]}
      }
    },
    {
      "Sid": "TagDiscovery",
      "Effect": "Allow",
      "Action": ["tag:GetResources"],
      "Resource": "*"
    },
    {
      "Sid": "RdsRead",
      "Effect": "Allow",
      "Action": ["rds:DescribeDBInstances", "rds:DescribeDBClusters"],
      "Resource": "*"
    },
    {
      "Sid": "EcsRead",
      "Effect": "Allow",
      "Action": ["ecs:DescribeServices"],
      "Resource": "*"
    },
    {
      "Sid": "Ec2Read",
      "Effect": "Allow",
      "Action": ["ec2:DescribeInstances"],
      "Resource": "*"
    },
    {
      "Sid": "Autoscaling",
      "Effect": "Allow",
      "Action": [
        "application-autoscaling:DescribeScalableTargets",
        "application-autoscaling:RegisterScalableTarget"
      ],
      "Resource": "*"
    },
    {
      "Sid": "Write",
      "Effect": "Allow",
      "Action": [
        "rds:StopDBInstance",
        "rds:StartDBInstance",
        "rds:StopDBCluster",
        "rds:StartDBCluster",
        "ecs:UpdateService",
        "ec2:StartInstances",
        "ec2:StopInstances"
      ],
      "Resource": "*"
    },
    {
      "Sid": "Notify",
      "Effect": "Allow",
      "Action": ["sns:Publish"],
      "Resource": "arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications"
    }
  ]
}
```

Applying this policy to the role is shown below.

```console
aws iam create-role --role-name cheapskate-reconciler \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name cheapskate-reconciler \
  --policy-name cheapskate --policy-document file://policy.json
```

What may be removed is the Actions for the resource types not managed, and `Notify` when no SNS topic is used.

### Why the DynamoDB permissions are split

The reconciler writes `status#` items and nothing else. It is therefore granted no `dynamodb:PutItem` at all, and its `UpdateItem` is confined to `status#*` by a `dynamodb:LeadingKeys` condition. `Scan` cannot be combined with a `LeadingKeys` condition, so it sits in a separate read-only statement.

Merging them into an unconditional `["dynamodb:Scan", "dynamodb:GetItem", "dynamodb:UpdateItem"]` works, but the guarantee that the function cannot rewrite a schedule is lost.

### Why Resource is a wildcard

`tag:GetResources`, the `Describe*` calls, and the Application Auto Scaling APIs all lack support for resource-level restrictions.

### Narrowing what may be stopped and started

Add the following condition to the `Write` statement and apply the same tag to the managed resources. The tag key and value are arbitrary.

```json
"Condition": {"StringEquals": {"aws:ResourceTag/cheapskate:managed": "true"}}
```

Making the value an array turns it into an OR. An OR across several tags is expressed by duplicating the statement. Several keys within one `Condition` are ANDed.

## 5. Reconciler Lambda function

What the function is required to be configured with is given below.

| Setting | Value |
| --- | --- |
| Package type | `Image` (the URI from §1) |
| Architecture | `arm64` (or `x86_64` — match the image's platform) |
| Memory / timeout | 256 MB / 120 seconds |
| Reserved concurrency | 1 (prevents concurrent reconciles) |

The environment variables to set on the function, with their defaults, are listed in [config.md](config.md).

```console
aws lambda create-function --function-name cheapskate-reconciler \
  --package-type Image \
  --code ImageUri=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-reconciler:v0.1.0 \
  --architectures arm64 --memory-size 256 --timeout 120 \
  --role arn:aws:iam::123456789012:role/cheapskate-reconciler \
  --environment "Variables={STATE_TABLE_NAME=cheapskate-state,NOTIFICATION_TOPIC_ARN=arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications,DEFAULT_TIMEZONE=Asia/Tokyo}"
aws lambda put-function-concurrency --function-name cheapskate-reconciler \
  --reserved-concurrent-executions 1
```

> [!NOTE]
> Pulling from ECR in the same account needs no repository policy. The principal creating the function does, however, need `ecr:BatchGetImage` and `ecr:GetDownloadUrlForLayer`.

Creating the log group in advance and setting a retention period is recommended; the Lambda default is to keep logs forever.

```console
aws logs create-log-group --log-group-name /aws/lambda/cheapskate-reconciler
aws logs put-retention-policy --log-group-name /aws/lambda/cheapskate-reconciler --retention-in-days 30
```

## 6. Periodic reconcile trigger

Invoke the function every N minutes with the payload `{}` (5 minutes recommended). Using EventBridge Scheduler, the settings are given below.

| Item | Value |
| --- | --- |
| Execution role trust policy | `scheduler.amazonaws.com` |
| Execution role permissions | `lambda:InvokeFunction` on the function ARN (and on `<arn>:*` when a qualifier is used) |
| Schedule expression | `rate(5 minutes)` |
| `FlexibleTimeWindow` | `OFF` |
| Target | The function, with `Input: {}` |

As a guard against the confused deputy problem, adding `"Condition": {"StringEquals": {"aws:SourceAccount": "123456789012"}}` to the trust policy is recommended.

```console
aws scheduler create-schedule --name cheapskate-reconcile \
  --schedule-expression "rate(5 minutes)" \
  --flexible-time-window Mode=OFF \
  --target '{"Arn":"arn:aws:lambda:ap-northeast-1:123456789012:function:cheapskate-reconciler","RoleArn":"arn:aws:iam::123456789012:role/cheapskate-scheduler","Input":"{}"}'
```

A `rate()` expression on an EventBridge rule works too. In that case, a Lambda resource-based policy as in §7 takes the place of the role.

## 7. EventBridge rule (RDS auto-start)

When AWS automatically starts a stopped RDS resource, this triggers a reconcile without waiting for the next periodic cycle. Only the start-completed events are subscribed to: at `RDS-EVENT-0153` / `RDS-EVENT-0154`, which announce that an auto-start has begun, the resource is still `starting` and the stop API cannot be called, so an invocation at that point would always come to nothing.

```json
{
  "source": ["aws.rds"],
  "detail-type": ["RDS DB Instance Event", "RDS DB Cluster Event"],
  "detail": {
    "EventID": ["RDS-EVENT-0088", "RDS-EVENT-0151"]
  }
}
```

The target is the function, and the event is passed through unmodified. Invocation is permitted through the Lambda's resource-based policy; no IAM role is used on the rule's target.

```console
aws events put-rule --name cheapskate-rds-events --event-pattern file://pattern.json
aws events put-targets --rule cheapskate-rds-events \
  --targets '[{"Id":"reconciler","Arn":"arn:aws:lambda:ap-northeast-1:123456789012:function:cheapskate-reconciler"}]'
aws lambda add-permission --function-name cheapskate-reconciler \
  --statement-id cheapskate-rds-events --action lambda:InvokeFunction \
  --principal events.amazonaws.com \
  --source-arn arn:aws:events:ap-northeast-1:123456789012:rule/cheapskate-rds-events
```

## 8. Invocation payloads

Any JSON object triggers a full reconcile, the `{}` of the scheduled run and the RDS events alike. The content of the payload never changes the scope of the work.

## 9. Web console (optional)

A component for performing the same operations as `cheapskate-cli` from a browser. It can also be run locally instead of deployed.

> [!WARNING]
> Access control is the IP allowlist alone; there is no login. Everyone inside an allowed CIDR can operate it. If that is unacceptable, do not build this section.

### Lambda function

A separate function using the `cheapskate-webconsole` image pushed in §1. Being a dedicated image, it needs no `ImageConfig.EntryPoint` override. Memory and timeout are 128 MB / 29 seconds. The environment variables are listed in [config.md](config.md); set `BASE_PATH` to the stage name below as `/<stage>`. The Lambda Web Adapter that converts events into HTTP is bundled in the image and needs neither a layer nor any configuration.

### Execution role

Grant only `dynamodb:Scan/GetItem/PutItem/DeleteItem` on the state table, `tag:GetResources`, the read-only `Describe*` calls below for displaying the current state, and the same `Logs` as in §4. Grant no RDS/ECS/EC2 control permissions, and no `dynamodb:UpdateItem` either — that is the only path by which a `status#` record can be written.

```json
{
  "Sid": "LiveStateRead",
  "Effect": "Allow",
  "Action": [
    "rds:DescribeDBInstances",
    "rds:DescribeDBClusters",
    "ecs:DescribeServices",
    "ec2:DescribeInstances"
  ],
  "Resource": "*"
}
```

If the console should query AWS not at all, this statement is unnecessary. The current-state column then shows access-denied on each row and the rest of the page still works. The Actions for unmanaged resource types may likewise be removed individually.

Without `tag:GetResources`, the group page shows a discovery error. The console itself still works.

### API Gateway REST API (v1)

Attach an `ANY` proxy integration to the Lambda on both the root resource and `{proxy+}`, and grant a Lambda permission to `apigateway.amazonaws.com`. Access control is the resource policy below.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": "*",
      "Action": "execute-api:Invoke",
      "Resource": "execute-api:/*"
    },
    {
      "Effect": "Deny",
      "Principal": "*",
      "Action": "execute-api:Invoke",
      "Resource": "execute-api:/*",
      "Condition": {
        "NotIpAddress": {
          "aws:SourceIp": ["203.0.113.0/24", "198.51.100.7/32"]
        }
      }
    }
  ]
}
```

Deploy to a stage of the same name as `BASE_PATH` (stage `console` with `BASE_PATH=/console`, for example). The URL is then `https://<api-id>.execute-api.<region>.amazonaws.com/console/`.

## 10. Entering the configuration

That completes the resources. At this point the state table is empty and the reconciler does nothing on every cycle. To decide what is managed, add a group configuration record and apply the selector tag to the AWS resources. For details, see the adding section in [operations.md](operations.md) and the selector tags in [resource_tag.md](resource_tag.md).
