# Runtime configuration

This document defines connection, time-zone, monitoring, and HTTP settings for the reconciler, CLI, and web console.

## Common settings

AWS credentials, region, and endpoints use AWS SDK configuration. Application environment variables shared across executables are:

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| STATE_TABLE_NAME | Reconciler and web console | None | DynamoDB table name |
| DEFAULT_TIMEZONE | No | UTC | IANA time zone for schedules and web dates |

An invalid DEFAULT_TIMEZONE prevents the reconciler and web console from starting. Use the same value in the CLI, reconciler, and web console.

## Reconciler

Logging, notification, and metrics settings are:

| Variable | Default | Purpose |
| --- | --- | --- |
| LOG_LEVEL | info | Minimum log level: debug, info, warn, or error |
| NOTIFICATION_TOPIC_ARN | Empty | SNS destination for successful actions |
| METRICS_ENABLED | false | Emit custom metrics when true |
| METRICS_NAMESPACE | cheapskate | CloudWatch custom metric namespace |

LOG_LEVEL is case-insensitive and defaults to info when empty. An unparseable value prevents startup.

An unparseable METRICS_ENABLED prevents startup. Empty METRICS_NAMESPACE selects cheapskate. For emitted data, see [Logs, metrics, and notifications](monitoring.md).

## CLI

The CLI saves named contexts and the current selection in a local JSON file. A context contains the DynamoDB table name and optional AWS profile, region, and schedule time zone. The table name comes from -table, then the selected context, then CHEAPSKATE_TABLE, then STATE_TABLE_NAME. A context's time zone takes precedence over DEFAULT_TIMEZONE. CLI override expiration output always uses UTC RFC 3339. For context commands, file locations, arguments, and output formats, see [CLI reference](cli.md).

## Web console

HTTP listen and public URL settings are:

| Variable | Default | Purpose |
| --- | --- | --- |
| PORT | 8000 in the image; unset for a standalone binary | Overrides -addr and binds to 127.0.0.1 |
| AWS_LWA_PORT | 8000 in the image | Lambda Web Adapter destination; must match PORT |
| BASE_PATH | Empty | Public URL path prefix |
| ALLOWED_HOSTS | Loopback hosts with the listen port | Comma-separated permitted Host values |

### Listen address and paths

A standalone binary without PORT uses -addr, defaulting to 127.0.0.1:8080.

BASE_PATH prefixes links, form destinations, and redirects, not incoming routes. An ingress exposing a prefixed URL must forward paths with the prefix removed.

### Public hosts

ALLOWED_HOSTS replaces the default list. When unset or empty, localhost, 127.0.0.1, and [::1] are allowed with the listen port. A value containing only whitespace or commas prevents startup.

Host matching is case-insensitive, exact, and includes any port. Wildcards and subdomain matching are unsupported. An unlisted Host returns HTTP 403 before reading or changing state.

With API Gateway or another ingress, list the public Host values actually forwarded. Do not include a scheme or path. For example:

```text
ALLOWED_HOSTS=console.example,abc123.execute-api.ap-northeast-1.amazonaws.com
```

For local access on port 8080, use:

```text
ALLOWED_HOSTS=localhost:8080,127.0.0.1:8080
```
