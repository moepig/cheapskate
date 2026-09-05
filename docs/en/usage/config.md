# Runtime configuration

The executables use the following environment variables.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `STATE_TABLE_NAME` | reconciler and web console | — | DynamoDB table name |
| `DEFAULT_TIMEZONE` | no | `UTC` | IANA time zone for all schedules and web dates |
| `NOTIFICATION_TOPIC_ARN` | no | empty | Destination for successful-action notifications |
| `METRICS_ENABLED` | no | `false` | Emit custom metrics when `true` |
| `METRICS_NAMESPACE` | no | `cheapskate` | Custom metric namespace |
| `PORT` | web-console image only | `8000` | HTTP listen port used with Lambda Web Adapter |
| `BASE_PATH` | web console only | empty | URL path prefix |

`DEFAULT_TIMEZONE` is validated at reconciler and web-console startup with the Go time-zone database. An invalid value prevents startup. The CLI reads the same variable and also defaults to UTC.

The CLI table name can be supplied with `-table`; otherwise it reads `CHEAPSKATE_TABLE` and then `STATE_TABLE_NAME`.

An invalid `METRICS_ENABLED` value prevents reconciler startup. Empty `METRICS_NAMESPACE` selects `cheapskate`.
