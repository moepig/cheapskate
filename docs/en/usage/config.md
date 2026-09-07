# Runtime configuration

The executables use the following environment variables.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `STATE_TABLE_NAME` | reconciler and web console | — | DynamoDB table name |
| `DEFAULT_TIMEZONE` | no | `UTC` | IANA time zone for all schedules and web dates |
| `NOTIFICATION_TOPIC_ARN` | no | empty | Destination for successful-action notifications |
| `METRICS_ENABLED` | no | `false` | Emit custom metrics when `true` |
| `METRICS_NAMESPACE` | no | `cheapskate` | Custom metric namespace |
| `PORT` | no | `8000` in image; unset for standalone binary | Web-console HTTP port; overrides `-addr` and binds to `127.0.0.1` |
| `AWS_LWA_PORT` | no | `8000` in image | Lambda Web Adapter destination port; must match `PORT` |
| `BASE_PATH` | no | empty | URL path prefix |

`DEFAULT_TIMEZONE` is validated at reconciler and web-console startup with the Go time-zone database. An invalid value prevents startup. The CLI reads the same variable and also defaults to UTC.

Only the reconciler uses NOTIFICATION_TOPIC_ARN, METRICS_ENABLED, and METRICS_NAMESPACE. PORT and BASE_PATH apply to the web console.

The CLI table name can be supplied with `-table`; otherwise it reads `CHEAPSKATE_TABLE` and then `STATE_TABLE_NAME`.

An invalid `METRICS_ENABLED` value prevents reconciler startup. Empty `METRICS_NAMESPACE` selects `cheapskate`.

For a standalone web-console binary without PORT, -addr selects the listen address and defaults to 127.0.0.1:8080. BASE_PATH prefixes external links, form destinations, and redirects, not incoming routes. An ingress exposing a prefixed URL must forward the path without that prefix.
