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
| `ALLOWED_HOSTS` | no | loopback hosts with the listen port | Comma-separated list of permitted web-console Host values |

`DEFAULT_TIMEZONE` is validated at reconciler and web-console startup with the Go time-zone database. An invalid value prevents startup. The CLI reads the same variable and also defaults to UTC.

Only the reconciler uses NOTIFICATION_TOPIC_ARN, METRICS_ENABLED, and METRICS_NAMESPACE. PORT, BASE_PATH, and ALLOWED_HOSTS apply to the web console.

The CLI table name can be supplied with `-table`; otherwise it reads `CHEAPSKATE_TABLE` and then `STATE_TABLE_NAME`.

An invalid `METRICS_ENABLED` value prevents reconciler startup. Empty `METRICS_NAMESPACE` selects `cheapskate`.

For a standalone web-console binary without PORT, -addr selects the listen address and defaults to 127.0.0.1:8080. BASE_PATH prefixes external links, form destinations, and redirects, not incoming routes. An ingress exposing a prefixed URL must forward the path without that prefix.

ALLOWED_HOSTS replaces the default allowlist. When unset or empty, the console permits localhost, 127.0.0.1, and [::1], each with the listen port. Every request must match a listed Host value, including any port, ignoring case. Wildcards and subdomain matching are unsupported. An unlisted Host returns HTTP 403 before reading or changing state. A value containing only whitespace or commas prevents startup.

For API Gateway or another ingress, set ALLOWED_HOSTS to the external Host values forwarded to the console. For multiple domains, use a comma-separated value such as console.example,abc123.execute-api.ap-northeast-1.amazonaws.com. For local access on port 8080, a value such as localhost:8080,127.0.0.1:8080 permits both names. Specify Host values without a scheme or URL path.
