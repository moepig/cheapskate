# Setup

## 1. Create the DynamoDB table

Create a table with String partition key `pk` and String sort key `sk`. Use on-demand or provisioned capacity as appropriate. Do not enable TTL and do not create secondary indexes.

```console
aws dynamodb create-table \
  --table-name cheapskate \
  --billing-mode PAY_PER_REQUEST \
  --attribute-definitions AttributeName=pk,AttributeType=S AttributeName=sk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE
```

## 2. Deploy the reconciler

Deploy the reconciler container image as a Lambda function. Set `STATE_TABLE_NAME` and optionally the other variables from [Configuration](config.md). Choose a timeout after testing a full cycle with the maximum expected tagged-resource count; Lambda permits at most 900 seconds.

Create one EventBridge Scheduler or EventBridge rule that invokes it every five minutes. Any payload is acceptable because every invocation performs a full reconcile.

The reconciler DynamoDB policy needs only a strongly consistent Query restricted to the `CONFIG` partition:

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

Also grant `tag:GetResources`, the required read and change calls for enabled RDS, ECS, Application Auto Scaling, and EC2 adapters, CloudWatch Logs delivery, and optional `sns:Publish` to the configured topic. See [AWS resources and IAM](../architecture/aws_resources.md).

## 3. Install an administration interface

Run `cheapskate-cli` from a trusted operator environment, or deploy the optional web-console image behind Lambda Web Adapter and an authenticated API Gateway or equivalent ingress.

The CLI and web console require these DynamoDB actions on the table:

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

For `show` and detail pages, also grant `tag:GetResources` and the supported services' read-only Describe calls. Do not manage group items directly through infrastructure as code; use the CLI or web console so full validation and conditional writes are applied.

## 4. Tag resources and create a group

Apply `cheapskate:group=<group>` to every resource, add the required ECS restoration tags where applicable, and create a schedule or override:

```console
cheapskate-cli -table cheapskate schedule --group dev \
  -start '0 8 * * 1-5' -stop '0 20 * * 1-5'
```

Invoke the reconciler once, inspect logs and the live observations in `show`, and then enable the five-minute schedule.

cheapskate itself does not create CloudWatch log groups, alarms, dashboards, SNS topics, or notification subscriptions.
