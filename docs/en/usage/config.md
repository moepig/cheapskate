# Environment variable reference

This document specifies the environment variables read by the reconciler, the web console, and `cheapskate-cli`.

## Reconciler

The variables it reads are given below. It reads no others.

| Variable | Required | Meaning |
| --- | --- | --- |
| `STATE_TABLE_NAME` | yes | DynamoDB table name |
| `NOTIFICATION_TOPIC_ARN` | no | SNS topic ARN. Empty or unset disables notifications |
| `DEFAULT_TIMEZONE` | no | The IANA timezone used for cron evaluation (default `UTC`) |
| `METRICS_ENABLED` | no | Whether to emit CloudWatch metrics (default `true`). A boolean (`true`/`false`, `1`/`0`) |
| `METRICS_NAMESPACE` | no | The namespace for the CloudWatch metrics (default `cheapskate`) |

A value of `METRICS_ENABLED` that cannot be interpreted (`fasle`, say) fails startup rather than falling back to the default. `METRICS_NAMESPACE` yields the default `cheapskate` whether it is unset or empty, so use `METRICS_ENABLED` to turn metrics off.

Disabling metrics loses the trends in failure and action counts. Detecting failures at all remains with the built-in Lambda `Errors` metric and the SNS notifications. While disabled, one `metrics-disabled` log line is written per cold start.

## Web console

The variables it reads are given below.

| Variable | Required | Meaning |
| --- | --- | --- |
| `STATE_TABLE_NAME` | yes | DynamoDB table name (`CHEAPSKATE_TABLE` also works) |
| `DEFAULT_TIMEZONE` | no | The IANA timezone used to display crons (defaults to the server's local time) |
| `BASE_PATH` | no | The base path including the API Gateway stage name (for example `/console`). Unset means the root |
| `PORT` | no | The listen port (bound to `127.0.0.1`). Unset means the value of the `-addr` flag (default `127.0.0.1:8080`) |

> [!NOTE]
> `PORT` is already set in the container image and is not something to specify when deploying to Lambda.

## cheapskate-cli

The variables it reads are given below.

| Variable | Required | Meaning |
| --- | --- | --- |
| `CHEAPSKATE_TABLE` | no | DynamoDB table name (the `-table` flag works too) |
