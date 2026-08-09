# cheapskate-cli architecture

`cheapskate-cli` is the CLI for operating group configuration. What it really does is CRUD on DynamoDB items, exposed through verbs named for intent.

## Scope of access

What it reaches and what it does there are given below.

| Target | Operations |
|---|---|
| DynamoDB state table | Reads, writes, and deletes on `group#`/`override#`; reads on `status#` and deletion of orphans |
| Resource Groups Tagging API | `tag:GetResources` (read-only) |
| RDS / ECS / EC2 | `Describe*` (read-only) |

It never calls a control API.

## Layer composition

The packages it uses and their roles are given below.

| Package | Role |
|---|---|
| `internal/app/groups` | Configuration validation and store calls. Shared with the web console |
| `internal/app/doctor` | State table diagnosis and orphan pruning. Shared with the web console |
| `internal/state` | The DynamoDB access layer. Shared with the reconciler |
| `internal/aws/tagging` | Discovery. The same `Discoverer` the reconciler uses |

It carries no data-access code of its own.

## Commands and the item operations behind them

The item operations each command performs, and whether it discovers, are collected below.

| Command | What it does | Discovery |
|---|---|---|
| `set-selector` | Sets the selector on `group#`, creating the group with `mode: disabled` if absent | No |
| `pin` / `unpin` / `schedule` / `disable` | Updates `mode` and the related attributes on `group#` | No |
| `override` / `clear-override` | PUT (with a TTL) and DELETE on `override#` | No |
| `list` | A single Scan of the whole table | No |
| `show` | One group's configuration + override + the discovered resources, with their states | That group only |
| `remove` | Deletes the group's `group#`/`override#`/`status#group#` | No |
| `doctor` | Diagnoses inconsistencies in the table; deletes orphans only with `--prune` | Every group |

`doctor` discovers for every group because whether a record is orphaned is settled by nothing other than the fact that it matches no group's selector. For details, see the state table diagnosis in [overview.md](overview.md).

## Output

The output is uniformly JSON. Every command writes exactly one JSON object to stdout, and there is no human-formatted text.

The destination and exit code for each path are given below.

| Path | Output | Exit code |
|---|---|---|
| Success | One object on stdout; the `command` field names the command | 0 |
| Failure | `{"error": "..."}` on stderr | 1 |
| Discovery failure (`show`) | One object on stdout including a `discover_error` field | 0 |
| Usage (`-h` / `-help`) | Text on stdout | 0 |

> [!NOTE]
> The `flag` package's own error output is discarded, so misusing a flag produces the same JSON error.

The DynamoDB item shapes are not exposed as they are; they are repacked into output types. The storage-driven `pk` is not printed, the selector appears as one object rather than three attributes, and `expires_at` appears as RFC3339 UTC rather than epoch seconds.

## Validation

Before writing, it validates the `desired` value, the cron expressions, the timezone, and the selector (rejecting tag keys starting with `aws:`, requiring the types to be a subset of the known ones, and requiring a non-empty tag value).

## Connection

How the endpoint and credentials are supplied is given below.

| Item | How it is supplied |
|---|---|
| Table name | The `-table` flag or the `CHEAPSKATE_TABLE` environment variable |
| Credentials, region, endpoint | The AWS SDK's standard chain. `AWS_ENDPOINT_URL` points it at a local emulator |
| tzdata | Embedded in the binary |

## Choosing between this and IaC

`group#` items can be managed with IaC. The reconciler never writes to `group#`, so there is no drift. `override#` expires through its TTL and is therefore not something to manage with IaC. Permanent configuration belongs to IaC or `cheapskate-cli`; temporary intervention belongs to an override.
