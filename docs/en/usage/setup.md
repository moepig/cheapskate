# Hosting cheapskate in your AWS account

No IaC template is distributed; this page is the complete contract for creating the resources yourself. Every setting, IAM policy, and payload cheapskate depends on is listed here — naming and tooling (Terraform, CDK, CloudFormation, console, plain CLI) are entirely yours. The example commands use the AWS CLI so they map 1:1 to any IaC tool; placeholders use account `123456789012` and region `ap-northeast-1`.

Resources to create: a DynamoDB table, the reconciler Lambda (container image) with its execution role, a periodic trigger, an EventBridge rule for RDS events, and optionally an SNS topic, a log group, and the web console.

## 1. Build and push the container images

The reconciler and the optional web console are separate images, one binary each, and neither is pulled from a public registry — build them from this repository and push them to ECR repositories in your own account:

```console
aws ecr create-repository --repository-name cheapskate-reconciler   # once
aws ecr create-repository --repository-name cheapskate-webconsole   # once, only for §9
make push \
  ECR_REPO_RECONCILER=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-reconciler \
  ECR_REPO_WEBCONSOLE=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-webconsole \
  TAG=v0.1.0
```

Skipping the web console? `make push-reconciler ECR_REPO_RECONCILER=... TAG=v0.1.0` is the whole step.

The default platform is `linux/arm64` (cross-compiled on any host, no emulation). Use `make push PLATFORM=linux/amd64 ...` and Lambda architecture `x86_64` if you need x86. Reference the pushed URIs from the functions, ideally by digest. See [../development/build.md](../development/build.md) for details.

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

Separately from notifications, alarm on the Lambda `Errors` metric: it covers cycle-wide failures (bad payload, `Scan` failure, timeout, panic) **and** any cycle with at least one resource-level failure. For counts rather than a yes/no, the reconciler emits EMF metrics every cycle under the `METRICS_NAMESPACE` namespace (default `cheapskate`, no dimensions, unit Count) — via CloudWatch Logs, so no `PutMetricData` call and no extra IAM permission:

| Metric | Meaning |
|---|---|
| `ReconciledResources` | Resources processed this cycle |
| `ReconcileActions` | start/stop calls made |
| `ReconcileErrors` | Resource-level and group-level failures |
| `ReconcileAborted` | 1 when the cycle never got going, otherwise 0 |

Those four are billed as custom metrics (a little over $1/month in total). Turn them off with `METRICS_ENABLED=false` (the namespace lives in a separate `METRICS_NAMESPACE`, default `cheapskate`). Disabling costs you the counts and trends only; failure *detection* still runs through the Lambda `Errors` metric and SNS.

**With neither a topic nor an alarm, every resource could be failing and nothing would fire.** See [troubleshooting.md](troubleshooting.md).

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
    {"Sid": "StateRead", "Effect": "Allow",
     "Action": ["dynamodb:Scan", "dynamodb:GetItem"],
     "Resource": "arn:aws:dynamodb:ap-northeast-1:123456789012:table/cheapskate-state"},
    {"Sid": "StateWriteStatusOnly", "Effect": "Allow",
     "Action": ["dynamodb:UpdateItem"],
     "Resource": "arn:aws:dynamodb:ap-northeast-1:123456789012:table/cheapskate-state",
     "Condition": {"ForAllValues:StringLike": {"dynamodb:LeadingKeys": ["status#*"]}}},
    {"Sid": "TagDiscovery", "Effect": "Allow",
     "Action": ["tag:GetResources"],
     "Resource": "*"},
    {"Sid": "RdsRead", "Effect": "Allow",
     "Action": ["rds:DescribeDBInstances", "rds:DescribeDBClusters"],
     "Resource": "*"},
    {"Sid": "EcsRead", "Effect": "Allow",
     "Action": ["ecs:DescribeServices"],
     "Resource": "*"},
    {"Sid": "Ec2Read", "Effect": "Allow",
     "Action": ["ec2:DescribeInstances"],
     "Resource": "*"},
    {"Sid": "Autoscaling", "Effect": "Allow",
     "Action": ["application-autoscaling:DescribeScalableTargets",
                "application-autoscaling:RegisterScalableTarget"],
     "Resource": "*"},
    {"Sid": "Write", "Effect": "Allow",
     "Action": ["rds:StopDBInstance", "rds:StartDBInstance",
                "rds:StopDBCluster", "rds:StartDBCluster", "ecs:UpdateService",
                "ec2:StartInstances", "ec2:StopInstances"],
     "Resource": "*"},
    {"Sid": "Notify", "Effect": "Allow",
     "Action": ["sns:Publish"],
     "Resource": "arn:aws:sns:ap-northeast-1:123456789012:cheapskate-notifications"}
  ]
}
```

- The reconciler owns only the `status#` items: group config and overrides belong to `cheapskate-cli` and the web console, and it reads them without ever writing them. `dynamodb:PutItem` is therefore not granted at all — the reconciler only ever `UpdateItem`s a status record — and the `dynamodb:LeadingKeys` condition confines even that to `status#*`. `Scan` cannot carry a `LeadingKeys` condition, so it is a separate read-only statement. If you would rather keep one statement, use `["dynamodb:Scan", "dynamodb:GetItem", "dynamodb:UpdateItem"]` with no condition; you then lose the guarantee that a bug or a compromised function cannot rewrite your schedules.
- `tag:GetResources` is how group membership is resolved at reconcile time (and by `cheapskate-cli show` / the web console's group page) — it lists every resource matching a group's selector tag. It, `Describe*`, and Application Auto Scaling APIs do not support resource-level restriction; keep `"Resource": "*"` there.
- To restrict stop/start to opted-in resources, add to the `Write` statement: `"Condition": {"StringEquals": {"aws:ResourceTag/cheapskate:managed": "true"}}` (any tag key/value you choose; use an array of values for OR matching) and tag the managed RDS/ECS/EC2 resources accordingly. To allow any one of several tags, duplicate the `Write` statement per tag (multiple keys inside one `Condition` block are ANDed).
- If you don't manage some resource type, drop the corresponding actions (e.g. without ECS, remove `ecs:UpdateService` and the `EcsRead` / `Autoscaling` statements; without EC2, remove `ec2:DescribeInstances`, `ec2:StartInstances`, `ec2:StopInstances`).
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
| `METRICS_ENABLED` | no | Whether to emit EMF metrics (default `true`); a boolean (`true`/`false`, `1`/`0`). An unparseable value fails startup rather than defaulting — a typo must not silently keep the billing on |
| `METRICS_NAMESPACE` | no | CloudWatch namespace for the EMF metrics (default `cheapskate`) |

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

Triggers an immediate full reconcile when AWS force-starts a stopped instance/cluster, instead of waiting for the next scheduled cycle (up to 5 minutes). Only the start-**completed** events are subscribed: `RDS-EVENT-0153`/`RDS-EVENT-0154` announce that an auto-start has begun, and at that point the resource is `starting` and cannot be stopped, so an invocation there is guaranteed to do nothing. The invocation still reconciles every group — cheapskate has no way to scope to just the one resource an event names, since group membership is resolved by live tag discovery rather than a lookup table (see §8). Event pattern:

```json
{
  "source": ["aws.rds"],
  "detail-type": ["RDS DB Instance Event", "RDS DB Cluster Event"],
  "detail": {
    "EventID": ["RDS-EVENT-0088", "RDS-EVENT-0151"]
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

- **Periodic / manual full reconcile**: any JSON object without `"source": "aws.rds"` — canonically `{}`. Reconciles every group: resolves each group's selector via `tag:GetResources`, then converges every resource it currently matches.
- **RDS event**: the unmodified EventBridge event (`source: aws.rds`, `detail.SourceType: DB_INSTANCE|CLUSTER`, `detail.SourceIdentifier`). Also triggers a full reconcile — every group, not just the one the event names — since group membership is live-discovered rather than looked up by resource ID. The event payload is logged but otherwise unused.

## 9. Web console (optional)

A browser frontend for the same operations as `cheapskate-cli`. **Access control is an IP allowlist only — no login.** Anyone inside the allowed CIDRs can operate the console; skip this section if that trade-off doesn't fit. It can also run locally against the table without deploying anything (see [../development/run_local.md](../development/run_local.md)).

Components:

- **A second Lambda from the `cheapskate-webconsole` image** built and pushed in §1 — its own image, so no entrypoint override is involved. 128 MB / 29 s is plenty. Env vars: `STATE_TABLE_NAME`, `DEFAULT_TIMEZONE`, and `BASE_PATH` set to the browser-visible path prefix — i.e. `/<stage-name>` of the API below. The Lambda Web Adapter that turns invocations into HTTP requests ships inside the image: no layer to attach and nothing to configure (see [../../ja/architecture/web_console.md](../../ja/architecture/web_console.md)).
- **Execution role**: `dynamodb:Scan/GetItem/PutItem/DeleteItem` on the state table, `tag:GetResources` (needed to show a group's live-discovered member resources), the read-only `Describe*` set below (needed for the **Live** column), plus the same `Logs` statement as §4 — deliberately **no** RDS/ECS/EC2 control-plane write permissions, and deliberately no `dynamodb:UpdateItem`, which is the only way a status record can be written. Without `tag:GetResources` the console still works, but a group's page shows an inline discover error instead of its member resources (never a 500 — see [operations.md](operations.md)).

  ```json
  {"Sid": "LiveStateRead", "Effect": "Allow",
   "Action": ["rds:DescribeDBInstances", "rds:DescribeDBClusters",
              "ecs:DescribeServices", "ec2:DescribeInstances"],
   "Resource": "*"}
  ```

  These are the same read-only calls the reconciler makes, with no `Stop`/`Start`/`UpdateService` alongside them — the console is handed each resource type through the narrower `port.Describer`, which has no way to change anything. Drop the statement if you do not want the console reaching AWS at all; the **Live** column then shows the access-denied error inline per row and every other part of the page keeps working. Drop individual actions for resource types you do not manage.
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

1. Tag a running dev RDS instance with your selector's key/value, create a group with that selector (`cheapskate-cli set-selector --group dev --tag-key cheapskate:group --tag-value dev --types rds-instance`), then pin it stopped (`cheapskate-cli pin --group dev stopped`, see [operations.md](operations.md)) → within one interval it must transition to `stopping`, and the `status#` item must show `last_action: stop`. `cheapskate-cli show --group dev` must list the instance under `resources`.
2. Start it manually from the console → it must be stopped again within one interval (drift correction).
3. Add an ECS service to the group's selector's tag/types and set `mode: schedule` → desiredCount must flip at the cron boundaries (0 at stop, the `cheapskate/desired-count` tag's value — or 1 if the tag is absent — at start).
4. Add an EC2 instance to the group's selector's tag/types and pin it stopped → it must stop within one interval; terminating it must never produce an error (the reconciler treats a vanished/terminated resource as a harmless skip, not a failure).
5. Remove the selector tag from a resource → the next reconcile must neither act on it nor report an error (it simply stops showing up under the group).
6. If notifications are configured, each action must produce one SNS message; converged cycles must produce none.
