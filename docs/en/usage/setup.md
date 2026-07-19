# Hosting cheapskate in your AWS account

No IaC template is distributed; this page is the complete contract for creating the resources yourself. Every setting, IAM policy, and payload cheapskate depends on is listed here — naming and tooling (Terraform, CDK, CloudFormation, console, plain CLI) are entirely yours. The example commands use the AWS CLI so they map 1:1 to any IaC tool; placeholders use account `123456789012` and region `ap-northeast-1`.

Resources to create: a DynamoDB table, the reconciler Lambda (container image) with its execution role, a periodic trigger, an EventBridge rule for RDS events, and optionally an SNS topic, a log group, and the web console.

## 1. Build and push the container image

The image is never pulled from a public registry — build it from this repository and push it to an ECR repository in your own account:

```console
aws ecr create-repository --repository-name cheapskate   # once
make push ECR_REPO=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate TAG=v0.1.0
```

The default platform is `linux/arm64` (cross-compiled on any host, no emulation). Use `make push PLATFORM=linux/amd64 ...` and Lambda architecture `x86_64` if you need x86. Reference the pushed URI from the function, ideally by digest. See [../development/build.md](../development/build.md) for details.

## 2. DynamoDB state table

- Partition key: `pk` (String). No sort key, no GSIs.
- TTL enabled on attribute `expires_at` (used by `override#` items).
- Billing mode: your choice; on-demand recommended (traffic is tiny).

```console
aws dynamodb create-table --table-name cheapskate-state \
  --attribute-definitions AttributeName=pk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST
aws dynamodb update-time-to-live --table-name cheapskate-state \
  --time-to-live-specification "Enabled=true,AttributeName=expires_at"
```

## 3. SNS topic (optional)

Notifications are sent only when an action is taken or fails. Any topic works; the function only calls `sns:Publish`. Omit the topic (and the `NOTIFICATION_TOPIC_ARN` env var) to disable notifications.

```console
aws sns create-topic --name cheapskate-notifications
aws sns subscribe --topic-arn arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications \
  --protocol email --notification-endpoint ops@example.com
```

## 4. Lambda execution role

Trust `lambda.amazonaws.com`; attach this policy (the complete set — nothing else is called):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Sid": "Logs", "Effect": "Allow",
     "Action": ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"],
     "Resource": "arn:aws:logs:ap-northeast-1:123456789012:log-group:/aws/lambda/*"},
    {"Sid": "State", "Effect": "Allow",
     "Action": ["dynamodb:Scan", "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem"],
     "Resource": "arn:aws:dynamodb:ap-northeast-1:123456789012:table/cheapskate-state"},
    {"Sid": "RdsRead", "Effect": "Allow",
     "Action": ["rds:DescribeDBInstances", "rds:DescribeDBClusters"],
     "Resource": "*"},
    {"Sid": "EcsRead", "Effect": "Allow",
     "Action": ["ecs:DescribeServices"],
     "Resource": "*"},
    {"Sid": "Autoscaling", "Effect": "Allow",
     "Action": ["application-autoscaling:DescribeScalableTargets",
                "application-autoscaling:RegisterScalableTarget"],
     "Resource": "*"},
    {"Sid": "Write", "Effect": "Allow",
     "Action": ["rds:StopDBInstance", "rds:StartDBInstance",
                "rds:StopDBCluster", "rds:StartDBCluster", "ecs:UpdateService"],
     "Resource": "*"},
    {"Sid": "Notify", "Effect": "Allow",
     "Action": ["sns:Publish"],
     "Resource": "arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications"}
  ]
}
```

- `Describe*` and Application Auto Scaling APIs do not support resource-level restriction; keep `"Resource": "*"` there.
- To restrict stop/start to opted-in resources, add to the `Write` statement: `"Condition": {"StringEquals": {"aws:ResourceTag/cheapskate:managed": "true"}}` (any tag key/value you choose; use an array of values for OR matching) and tag the managed RDS/ECS resources accordingly. To allow any one of several tags, duplicate the `Write` statement per tag (multiple keys inside one `Condition` block are ANDed).
- If you don't manage some resource type, drop the corresponding actions (e.g. without ECS, remove `ecs:UpdateService` and the `EcsRead` / `Autoscaling` statements).
- Drop the `Notify` statement if you run without a topic.

```console
aws iam create-role --role-name cheapskate-reconciler \
  --assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name cheapskate-reconciler \
  --policy-name cheapskate --policy-document file://policy.json
```

## 5. Reconciler Lambda function

| Setting | Value |
|---|---|
| Package type | `Image`, URI from §1 |
| Architecture | `arm64` (or `x86_64` — must match the image platform) |
| Memory / timeout | 256 MB / 120 s |
| Reserved concurrency | 1 (prevents overlapping reconciles) |

Environment variables (the full contract — nothing else is read):

| Variable | Required | Meaning |
|---|---|---|
| `STATE_TABLE_NAME` | yes | DynamoDB table name |
| `NOTIFICATION_TOPIC_ARN` | no | SNS topic ARN; empty/unset disables notifications |
| `DEFAULT_TIMEZONE` | no | IANA timezone for cron evaluation (default `UTC`) |

```console
aws lambda create-function --function-name cheapskate-reconciler \
  --package-type Image \
  --code ImageUri=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate:v0.1.0 \
  --architectures arm64 --memory-size 256 --timeout 120 \
  --role arn:aws:iam::123456789012:role/cheapskate-reconciler \
  --environment "Variables={STATE_TABLE_NAME=cheapskate-state,NOTIFICATION_TOPIC_ARN=arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications,DEFAULT_TIMEZONE=Asia/Tokyo}"
aws lambda put-function-concurrency --function-name cheapskate-reconciler \
  --reserved-concurrent-executions 1
```

Pulling from a same-account ECR repository needs no repository policy; the principal creating the function needs `ecr:BatchGetImage` and `ecr:GetDownloadUrlForLayer` on the repository.

Recommended: create the log group yourself so retention is not the Lambda default of "never expire":

```console
aws logs create-log-group --log-group-name /aws/lambda/cheapskate-reconciler
aws logs put-retention-policy --log-group-name /aws/lambda/cheapskate-reconciler --retention-in-days 30
```

## 6. Periodic reconcile trigger

Invoke the function with payload `{}` every N minutes (5 is a good default). With EventBridge Scheduler:

- Execution role: trust `scheduler.amazonaws.com`, ideally with `"Condition": {"StringEquals": {"aws:SourceAccount": "123456789012"}}` (confused-deputy protection). Permissions: `lambda:InvokeFunction` on the function ARN (and `<arn>:*` if you use qualifiers).
- Schedule: `rate(5 minutes)`, `FlexibleTimeWindow: OFF`, target the function with `Input: {}`.

```console
aws scheduler create-schedule --name cheapskate-reconcile \
  --schedule-expression "rate(5 minutes)" \
  --flexible-time-window Mode=OFF \
  --target '{"Arn":"arn:aws:lambda:ap-northeast-1:123456789012:function:cheapskate-reconciler","RoleArn":"arn:aws:iam::123456789012:role/cheapskate-scheduler","Input":"{}"}'
```

A classic EventBridge rule with a `rate()` expression works just as well (then use a resource-based Lambda permission instead of a role, as in §7).

## 7. EventBridge rule (RDS auto-start fast path)

Reacts immediately when AWS force-starts a stopped instance/cluster, instead of waiting for the next cycle. Event pattern:

```json
{
  "source": ["aws.rds"],
  "detail-type": ["RDS DB Instance Event", "RDS DB Cluster Event"],
  "detail": {
    "EventID": ["RDS-EVENT-0154", "RDS-EVENT-0153", "RDS-EVENT-0088", "RDS-EVENT-0151"]
  }
}
```

Target: the function, event passed through unchanged. Grant invocation with a Lambda resource-based policy — no IAM role is involved for rule targets:

```console
aws events put-rule --name cheapskate-rds-events --event-pattern file://pattern.json
aws events put-targets --rule cheapskate-rds-events \
  --targets '[{"Id":"reconciler","Arn":"arn:aws:lambda:ap-northeast-1:123456789012:function:cheapskate-reconciler"}]'
aws lambda add-permission --function-name cheapskate-reconciler \
  --statement-id cheapskate-rds-events --action lambda:InvokeFunction \
  --principal events.amazonaws.com \
  --source-arn arn:aws:events:ap-northeast-1:123456789012:rule/cheapskate-rds-events
```

## 8. Invocation payloads (reference)

- **Periodic / manual full reconcile**: any JSON object without `"source": "aws.rds"` — canonically `{}`. Reconciles every `config#` item.
- **RDS event**: the unmodified EventBridge event (`source: aws.rds`, `detail.SourceType: DB_INSTANCE|CLUSTER`, `detail.SourceIdentifier`). Reconciles only that resource; unregistered resources are ignored.

## 9. Web console (optional)

A browser frontend for the same operations as `cheapskate-cli`. **Access control is an IP allowlist only — no login.** Anyone inside the allowed CIDRs can operate the console; skip this section if that trade-off doesn't fit. It can also run locally against the table without deploying anything (see [../development/run_local.md](../development/run_local.md)).

Components:

- **Second Lambda from the same image**, with entrypoint override `ImageConfig.EntryPoint: ["/var/runtime/webconsole"]`. 128 MB / 29 s is plenty. Env vars: `STATE_TABLE_NAME`, `DEFAULT_TIMEZONE`, and `BASE_PATH` set to the browser-visible path prefix — i.e. `/<stage-name>` of the API below.
- **Execution role**: `dynamodb:Scan/GetItem/PutItem/DeleteItem` on the state table plus the same `Logs` statement as §4 — deliberately no RDS/ECS permissions.
- **API Gateway REST API** (v1 — HTTP APIs have no resource policies) with `ANY` proxy integrations to the Lambda on both the root resource and a `{proxy+}` child, plus a Lambda permission for `apigateway.amazonaws.com`. The resource policy is the sole access control:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Principal": "*", "Action": "execute-api:Invoke", "Resource": "execute-api:/*"},
    {"Effect": "Deny", "Principal": "*", "Action": "execute-api:Invoke", "Resource": "execute-api:/*",
     "Condition": {"NotIpAddress": {"aws:SourceIp": ["203.0.113.0/24", "198.51.100.7/32"]}}}
  ]
}
```

Deploy to a stage whose name matches `BASE_PATH` (e.g. stage `console`, `BASE_PATH=/console`); the entry point is `https://<api-id>.execute-api.<region>.amazonaws.com/console/`.

## 10. Verify the deployment

1. Register a `config#` item with `mode: pinned`, `desired: stopped` for a running dev RDS instance (`cheapskate-cli pin rds-instance#<id> stopped`, see [operations.md](operations.md)) → within one interval it must transition to `stopping`, and the `status#` item must show `last_action: stop`.
2. Start it manually from the console → it must be stopped again within one interval (drift correction).
3. Register a `mode: schedule` ECS service → desiredCount must flip at the cron boundaries (0 at stop, `restore_count` at start).
4. If notifications are configured, each action must produce one SNS message; converged cycles must produce none.
