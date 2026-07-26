# Operating cheapskate

Desired state lives in `group#` and `override#` items in the DynamoDB state table: a **target group** carries the schedule/pin/override config plus a **selector** (an AWS resource tag key/value + a set of resource types). Membership is never registered explicitly — at reconcile time (and whenever `cheapskate-cli show` or the web console's group page is opened), cheapskate calls the read-only Resource Groups Tagging API (`tag:GetResources`) to find every resource currently matching the selector, live. Tag a resource with the selector's key/value and it is picked up on the next cycle; untag it and it drops out — no add/remove step in DynamoDB. Manage the group config with the bundled `cheapskate-cli` CLI, the web console, your IaC, or raw `put-item` calls — they are just data. Only the reconciler Lambda ever calls RDS/ECS/EC2 control APIs; `tag:GetResources` is the one read-only AWS API the CLI and web console also call (for `show` and the group page).

## The cheapskate-cli CLI

```console
go build -o cheapskate-cli ./cmd/cheapskate-cli        # or grab a release binary
export CHEAPSKATE_TABLE=<state-table-name>

# Define which AWS resources a group manages (creates the group, mode=disabled, on first use):
cheapskate-cli set-selector --group dev --tag-key cheapskate:group --tag-value dev --types rds-cluster,ecs-service,ec2-instance

# Keep everything the selector matches stopped indefinitely (survives the 7-day RDS auto-start):
cheapskate-cli pin --group dev stopped

# Release the pin — resumes mode=schedule if the group has cron settings, else mode=disabled:
cheapskate-cli unpin --group dev

# Run the group on weekdays 09:00-20:00 JST — every matched resource follows the same schedule:
cheapskate-cli schedule --group dev -start "0 9 * * MON-FRI" -stop "0 20 * * MON-FRI" -timezone Asia/Tokyo

# Start everything the selector matches temporarily despite its pin (expires automatically via TTL):
cheapskate-cli override --group dev running -for 2h
cheapskate-cli clear-override --group dev

cheapskate-cli list                 # every group's config/selector/override/status
cheapskate-cli show --group dev     # group config + override + live-discovered resources (with status)
cheapskate-cli disable --group dev  # keep the config, stop managing everything the selector matches

cheapskate-cli remove --group dev   # delete the group's config/override/status (matched AWS resources are untouched — just untag them to detach)

cheapskate-cli doctor               # diagnose state-table inconsistencies and leftovers (read-only)
cheapskate-cli doctor --prune       # ...and delete the unambiguously orphaned records
```

`--types` is a comma-separated list of `rds-instance`, `rds-cluster`, `ecs-service`, `ec2-instance`.

A resource that starts matching an already-scheduled or already-pinned group's selector (i.e. you just tagged it) is picked up on the *next* reconcile with no extra step — the group's config is what governs every matched resource, not a per-resource copy.

Operators running `cheapskate-cli` need `dynamodb:Scan`, `GetItem`, `PutItem`, and `DeleteItem` on the state table, plus `tag:GetResources` for `show` and `doctor` (an operator without it can still manage groups; `show` reports a discover error instead of the resource list, and `doctor` reports it as `discover-error` and skips the orphan check) — no RDS/ECS/EC2 control-plane permissions.

## CLI output

Every command prints exactly one JSON object on stdout, so the CLI is scriptable with `jq` without
any line-scraping. A failure prints `{"error": "..."}` on stderr and exits 1; the usage text
(`cheapskate-cli -h`) is the only non-JSON output.

```console
$ cheapskate-cli pin --group dev stopped
{
  "command": "pin",
  "group": "dev",
  "mode": "pinned",
  "desired": "stopped"
}

$ cheapskate-cli list | jq -r '.groups[] | select(.mode == "schedule") | .name'
dev

$ cheapskate-cli show --group dev | jq -c '.resources[] | {ref, live: .live.state}'
{"ref":"dev-cluster/api","live":"running"}
```

- `list` → `{"command": "list", "groups": [...]}`, one object per group with its config, `selector`,
  `override`, `status`, and — for a malformed item — that group's own `error` (the other groups are
  still listed).
- `show` → the same group object plus `resources` (always an array) and, when the Tagging API call
  failed, `discover_error` (still exit 0 — see [architecture](../architecture/cheapskate-cli.md)).
- Mutating commands (`set-selector`, `pin`, `unpin`, `schedule`, `disable`, `override`,
  `clear-override`, `remove`) → `command`, `group`, and the configuration that command wrote —
  e.g. `selector` plus `"created": true` for a `set-selector` that created the group, or
  `override.expires_at` (RFC3339 UTC) for `override`. They report what was written, not a re-read
  of the whole group; use `show` for that.

## From IaC or scripts

Terraform users can manage `group#` items with `aws_dynamodb_table_item`. The reconciler never writes to them, so drift detection stays clean. Keep using `cheapskate-cli override` for temporary operations — overrides are TTL'd and not meant to be IaC-managed. There is nothing to register for individual resources: just apply the selector's tag key/value to whatever RDS/ECS/EC2 resources should be managed (e.g. via your existing `aws_db_instance`/`aws_ecs_service`/`aws_instance` resource's `tags` block).

Equivalent raw group creation:

```console
aws dynamodb put-item --table-name <state-table-name> --item '{
  "pk":        {"S": "group#dev"},
  "mode":      {"S": "pinned"},
  "desired":   {"S": "stopped"},
  "tag_key":   {"S": "cheapskate:group"},
  "tag_value": {"S": "dev"},
  "types":     {"SS": ["rds-cluster"]}
}'
```

## Item reference

| Item | pk | Attributes |
|---|---|---|
| Group | `group#<name>` | `mode` (`pinned`\|`schedule`\|`disabled`), `desired` (pinned: `running`\|`stopped`), `start_cron`/`stop_cron` (schedule: 5-field cron; the most recently fired one wins), `timezone` (IANA name; defaults to the reconciler's `DEFAULT_TIMEZONE`), `tag_key`/`tag_value` (the AWS resource tag the selector matches), `types` (string set: `rds-instance`\|`rds-cluster`\|`ecs-service`\|`ec2-instance`) |
| Override | `override#<group-name>` | `desired`, `expires_at` (TTL) — applies to every resource the group's selector currently matches |
| Status (per resource) | `status#<type>#<identifier>` (ECS: `status#ecs-service#<cluster>/<service>`) | reconciler-owned per-resource audit trail (`observed_state`, `last_action(_at)`, `last_error(_at)`, plus `transitioning_since` while a transition is in progress). Orphaned once a resource stops matching any selector — harmless; clear them with `cheapskate-cli doctor --prune`, or by hand (`aws dynamodb delete-item --table-name <table> --key '{"pk":{"S":"status#..."}}'`) |
| Status (group-level) | `status#group#<name>` | same shape as per-resource status, used for group-level failures — a bad cron/timezone, the `tag:GetResources` call itself failing (e.g. missing IAM permission), or the group's selector overlapping one that another group already claimed |

## The web console

If deployed ([setup.md §9](setup.md#9-web-console-optional)), the console lists every group on the index page (config + selector + override + last error, one row each), and each group's page shows its live-discovered matched resources (type/name/last action/observed state/transitioning since/last error) plus pin/unpin/schedule/override/clear-override/disable/remove forms (applied to every matched resource) and a selector-editing form — all server-rendered without JavaScript. The **diagnostics** link in the header runs the same checks as `cheapskate-cli doctor`, prune included. A `tag:GetResources` failure (e.g. missing IAM permission) renders as an inline message on the group page rather than an error page. Remember: the IP allowlist is the only access control. It can also run locally with your own AWS credentials: `CHEAPSKATE_TABLE=<table> go run ./cmd/webconsole` serves on `127.0.0.1:8080` — or use `make dev` for a one-command local stack including a sample group (see [run_local.md](../development/run_local.md)).

## Monitoring and behavior notes

- Notifications (if a topic is configured) are sent only when an action is taken or fails, and include the group: `[cheapskate] stop: dev/rds-instance#dev-db`. Converged cycles are silent.
- For persistent failures, alarm on the Lambda `Errors` metric — it covers both cycle-wide failures and any cycle with resource-level failures — and read `ReconcileErrors` in the `cheapskate` EMF namespace for counts. The last error per resource is recorded in its `status#` item (`last_error`) and visible via `cheapskate-cli show --group`. A group-level problem (an invalid cron, the Tagging API call itself failing, an overlapping selector) is recorded once against `status#group#<name>` instead of being fanned out per resource — fixing it clears the error on the next converged cycle.
- Resources in transitional states (`starting`, `stopping`, …) are skipped and picked up on the next cycle. The first such cycle stamps `transitioning_since` on the status item and a settled observation clears it, so a transition that never finishes can be found (`cheapskate-cli doctor` reports it as `stuck-transitioning`) instead of being silently skipped forever.
- `cheapskate-cli doctor` diagnoses state-table inconsistencies — orphaned `override#`/`status#` records, corrupt or unusable group config, overlapping selectors, stuck transitions — and `--prune` deletes the unambiguously orphaned ones. See [troubleshooting.md](troubleshooting.md).
- A resource that disappears right after being discovered (deletion race, or Tagging API staleness — results can lag reality by a few minutes) is skipped, not treated as an error; it self-heals once discovery catches up.
- ECS start-up sizing comes from tags on the service itself, not from anything saved at stop time: `cheapskate/desired-count` sets the desiredCount cheapskate starts it back up to (default 1 if the tag is absent). Services with Application Auto Scaling additionally get min/max set to 0/0 on stop (a scaling policy would otherwise undo the desiredCount change) and set back on start from `cheapskate/scaling-min`/`cheapskate/scaling-max` (each independently defaults to the desired count if unset). The three tags must satisfy `scaling-min <= desired-count <= scaling-max`; a set that does not (min above max included) fails the start before any AWS call is made, because a desiredCount outside the bounds would be pulled straight back to them by Auto Scaling — the count you asked for would silently not happen. If the desiredCount update fails after the scalable target was zeroed, the target is rolled back to the values it had, so a failed stop does not leave a running service unable to scale out.
- A resource matched by more than one group's selector is claimed by whichever group sorts first by name; the losing group records the overlap against its own `status#group#<name>` (avoid overlapping selectors in the first place — `doctor` reports them).
- Crons are evaluated against the group's local wall clock, so in a `timezone` that observes DST, place them outside the transition window (01:00-03:00 in most regions). A cron sitting in the hour the spring-forward skips (02:00-02:59 in `America/New_York`) does not fire at all that day: a skipped `start_cron` leaves the group stopped for the day, a skipped `stop_cron` leaves it running until the next stop. The fall-back hour that happens twice may fire a cron twice, which is harmless — both firings resolve to the same desired state.
- Every invocation — the periodic schedule and the RDS auto-start EventBridge rule alike — does a full reconcile of every group. There is no scoped single-resource path: group membership is resolved by live discovery, so scoping would cost the same as reconciling everything.
