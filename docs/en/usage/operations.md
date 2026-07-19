# Operating cheapskate

Desired state lives in `config#` items in the DynamoDB state table. Manage them with the bundled `csctl` CLI, the web console, your IaC, or raw `put-item` calls — they are just data. Only the reconciler Lambda ever calls RDS/ECS APIs.

## The csctl CLI

```console
go build -o csctl ./cmd/csctl        # or grab a release binary
export CHEAPSKATE_TABLE=<state-table-name>

# Keep an Aurora cluster stopped indefinitely (survives the 7-day auto-start):
csctl pin rds-cluster#dev-aurora stopped

# Run an ECS service on weekdays 09:00-20:00 JST:
csctl schedule ecs#dev-cluster/api -start "0 9 * * MON-FRI" -stop "0 20 * * MON-FRI" \
  -timezone Asia/Tokyo -restore-count 2

# Start something temporarily despite its pin (expires automatically via TTL):
csctl override rds-cluster#dev-aurora running -for 2h

csctl list                           # all resources with override/last-action/observed state
csctl show rds-cluster#dev-aurora    # config + override + status as JSON
csctl disable ecs#dev-cluster/api    # keep the config, stop managing
csctl remove ecs#dev-cluster/api     # delete config, override, and status
```

Operators running `csctl` need only `dynamodb:Scan`, `GetItem`, `PutItem`, and `DeleteItem` on the state table — no RDS/ECS permissions.

## From IaC or scripts

Terraform users can manage config items with `aws_dynamodb_table_item`. The reconciler never writes to `config#` items, so drift detection stays clean. Keep using `csctl override` for temporary operations — overrides are TTL'd and not meant to be IaC-managed.

Equivalent raw registration:

```console
aws dynamodb put-item --table-name <state-table-name> --item '{
  "pk":      {"S": "config#rds-cluster#dev-aurora"},
  "type":    {"S": "rds-cluster"},
  "mode":    {"S": "pinned"},
  "desired": {"S": "stopped"}
}'
```

## Config item reference

| Attribute | Applies to | Meaning |
|---|---|---|
| `pk` | all | `config#<type-prefix>#<identifier>`; ECS uses `config#ecs#<cluster>/<service>` |
| `type` | all | `rds-instance` \| `rds-cluster` \| `ecs-service` |
| `mode` | all | `pinned` (fixed desired state) \| `schedule` (cron) \| `disabled` |
| `desired` | pinned | `running` \| `stopped` |
| `start_cron` / `stop_cron` | schedule | 5-field cron; the most recently fired one wins |
| `timezone` | schedule | IANA name; defaults to the `DEFAULT_TIMEZONE` env var of the reconciler |
| `restore_count` | ecs-service | desiredCount used on start; falls back to the count saved at stop time |

## The web console

If deployed ([setup.md §9](setup.md#9-web-console-optional)), the console offers the same operations (list, pin, schedule, override, disable, remove) in the browser, server-rendered without JavaScript. Remember: the IP allowlist is the only access control. It can also run locally with your own AWS credentials: `CHEAPSKATE_TABLE=<table> go run ./cmd/webconsole` serves on `127.0.0.1:8080`.

## Monitoring and behavior notes

- Notifications (if a topic is configured) are sent only when an action is taken or fails; converged cycles are silent.
- For persistent failures, alarm on the Lambda `Errors` metric; the last error per resource is recorded in its `status#` item (`last_error`) and visible via `csctl show`.
- Resources in transitional states (`starting`, `stopping`, …) are skipped and picked up on the next cycle.
- ECS services with Application Auto Scaling get min/max set to 0/0 on stop and restored on start (a scaling policy would otherwise undo desiredCount changes).
