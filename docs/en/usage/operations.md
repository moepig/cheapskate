# Operating cheapskate

Desired state lives in `tag#` and `member#` items in the DynamoDB state table: a **tag** carries the schedule/pin/override config once, and each **resource** joins a tag as a member. A resource belongs to exactly one tag. Manage them with the bundled `cheapskate-cli` CLI, the web console, your IaC, or raw `put-item` calls — they are just data. Only the reconciler Lambda ever calls RDS/ECS APIs.

## The cheapskate-cli CLI

```console
go build -o cheapskate-cli ./cmd/cheapskate-cli        # or grab a release binary
export CHEAPSKATE_TABLE=<state-table-name>

# Add resources to a tag (creates the tag, mode=disabled, on first add):
cheapskate-cli add --tag dev --type rds-cluster --name dev-aurora
cheapskate-cli add --tag dev --type ecs --cluster dev-cluster --service api -restore-count 2

# Keep everything in the tag stopped indefinitely (survives the 7-day auto-start):
cheapskate-cli pin --tag dev stopped

# Run the tag on weekdays 09:00-20:00 JST — every member follows the same schedule:
cheapskate-cli schedule --tag dev -start "0 9 * * MON-FRI" -stop "0 20 * * MON-FRI" -timezone Asia/Tokyo

# Start everything in the tag temporarily despite its pin (expires automatically via TTL):
cheapskate-cli override --tag dev running -for 2h

cheapskate-cli list                 # every tag with its members and state, resolved
cheapskate-cli show --tag dev       # tag config + override + members as JSON
cheapskate-cli disable --tag dev    # keep the config, stop managing every member

cheapskate-cli remove --tag dev --type ecs --cluster dev-cluster --service api  # drop one member
cheapskate-cli remove --tag dev                                                # delete the whole tag
```

Resource flags (`add`/`remove`): `--type rds-instance|rds-cluster|ecs`, `--name IDENTIFIER` for the RDS types, `--cluster CLUSTER --service SERVICE` for `ecs`, and `-restore-count N` (ecs only, per member).

A resource added to an already-scheduled or already-pinned tag is picked up on the *next* reconcile with no extra step — the tag's config is what governs every member, not a per-resource copy.

Operators running `cheapskate-cli` need only `dynamodb:Scan`, `GetItem`, `PutItem`, and `DeleteItem` on the state table — no RDS/ECS permissions.

## From IaC or scripts

Terraform users can manage `tag#`/`member#` items with `aws_dynamodb_table_item`. The reconciler never writes to them, so drift detection stays clean. Keep using `cheapskate-cli override` for temporary operations — overrides are TTL'd and not meant to be IaC-managed.

Equivalent raw registration (a tag plus one member):

```console
aws dynamodb put-item --table-name <state-table-name> --item '{
  "pk":      {"S": "tag#dev"},
  "mode":    {"S": "pinned"},
  "desired": {"S": "stopped"}
}'
aws dynamodb put-item --table-name <state-table-name> --item '{
  "pk":   {"S": "member#rds-cluster#dev-aurora"},
  "tag":  {"S": "dev"},
  "type": {"S": "rds-cluster"}
}'
```

## Item reference

| Item | pk | Attributes |
|---|---|---|
| Tag | `tag#<name>` | `mode` (`pinned`\|`schedule`\|`disabled`), `desired` (pinned: `running`\|`stopped`), `start_cron`/`stop_cron` (schedule: 5-field cron; the most recently fired one wins), `timezone` (IANA name; defaults to the reconciler's `DEFAULT_TIMEZONE`) |
| Member | `member#<type-prefix>#<identifier>` (ECS: `member#ecs#<cluster>/<service>`) | `tag` (the owning tag name), `type` (`rds-instance`\|`rds-cluster`\|`ecs-service`), `restore_count` (ecs-service only: desiredCount used on start, falls back to the count saved at stop time) |
| Override | `override#<tag-name>` | `desired`, `expires_at` (TTL) — applies to every member of the tag |
| Status | `status#<type-prefix>#<identifier>` | reconciler-owned per-resource audit + ECS restore data; unchanged by the tag model |

## The web console

If deployed ([setup.md §9](setup.md#9-web-console-optional)), the console groups resources by tag: the index lists every tag with its members, and each tag's page offers pin/schedule/override/disable/remove (applied to every member) plus per-member add/remove — all server-rendered without JavaScript. Remember: the IP allowlist is the only access control. It can also run locally with your own AWS credentials: `CHEAPSKATE_TABLE=<table> go run ./cmd/webconsole` serves on `127.0.0.1:8080` — or use `make dev` for a one-command local stack including a sample tag (see [run_local.md](../development/run_local.md)).

## Monitoring and behavior notes

- Notifications (if a topic is configured) are sent only when an action is taken or fails, and now include the tag: `[cheapskate] stop: dev/rds-instance#dev-db`. Converged cycles are silent.
- For persistent failures, alarm on the Lambda `Errors` metric; the last error per resource is recorded in its `status#` item (`last_error`) and visible via `cheapskate-cli show --tag`.
- Resources in transitional states (`starting`, `stopping`, …) are skipped and picked up on the next cycle.
- ECS services with Application Auto Scaling get min/max set to 0/0 on stop and restored on start (a scaling policy would otherwise undo desiredCount changes).
- A tag-level problem (an invalid cron, say) is recorded and notified once per member — fixing the tag clears every member's error on the next converged cycle.
