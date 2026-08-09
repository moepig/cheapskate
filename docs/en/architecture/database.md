# DynamoDB table structure

The key layout and item shapes of the single DynamoDB table that holds the state. The schema, the key layout, and the Go representation of the items are all closed inside `internal/state`; `internal/core/model` holds domain types only and knows nothing of the storage format.

## Table definition

What the table is required to provide is given below.

| Item | Value |
|---|---|
| Partition key | `pk` (String) only |
| Sort key | none |
| GSI / LSI | none |
| Billing mode | any (on-demand recommended) |
| TTL | attribute `expires_at` (Number, epoch seconds). Only `override#` items carry it |

With no sort key, the item kind and its subject are encoded entirely in the `pk` string: a prefix (`group#` / `override#` / `status#`) and what follows it.

## The item kinds

Four kinds of item are stored. The form of the `pk` for each, together with its writers and readers, is collected below.

| Item | `pk` form | Writer | Reader |
|---|---|---|---|
| Group configuration | `group#<name>` | `cheapskate-cli` / web console | reconciler, CLI, web console |
| Override | `override#<name>` | Same as above | Same as above |
| Status (per resource) | `status#<type>#<ref>` | reconciler | CLI, web console |
| Status (per group) | `status#group#<name>` | reconciler | CLI, web console |

## group# — group configuration

The domain representation is `model.GroupSpec`. The group name is held in the `Name` field rather than derived from the `pk`. The attributes are given below.

| Attribute | Type | Meaning |
|---|---|---|
| `pk` | S | `group#<name>` |
| `mode` | S | `pinned` \| `schedule` \| `disabled`. Unset is treated as `disabled` |
| `desired` | S | Meaningful only with `mode: pinned`. `running` \| `stopped` |
| `start_cron` / `stop_cron` | S | Only with `mode: schedule`. Five-field cron expressions |
| `timezone` | S | IANA timezone name. Unset falls back to the reconciler's `DEFAULT_TIMEZONE` |
| `tag_key` / `tag_value` | S | The selector's tag condition |
| `types` | SS | The resource types the selector targets |

A group name matches `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`. Neither `#` nor `/` may appear: `#` is the `pk` separator, and `/` can collide with an ECS `ref`.

A group may be created with no selector (`tag_key`, `tag_value`, and `types` all empty), but setting `mode` to `pinned` or `schedule` requires one. An empty StringSet cannot be represented in DynamoDB, so with no selector the `types` attribute is omitted entirely.

## override# — a time-limited override

The domain representation is `model.Override`. The attributes are given below.

| Attribute | Type | Meaning |
|---|---|---|
| `pk` | S | `override#<name>` |
| `desired` | S | `running` \| `stopped` |
| `expires_at` | N | epoch seconds. The attribute DynamoDB TTL acts on |

The reading side ignores an item with `expires_at <= now` as expired, because TTL deletion is asynchronous and can lag by up to 48 hours. The TTL itself exists only to tidy away the leftover items.

## `status#<type>#<ref>` — per-resource results

The domain representation is `model.Status`. The values are a snapshot taken when the last action or error occurred, not a live state. The attributes are given below.

| Attribute | Type | Meaning |
|---|---|---|
| `observed_state` | S | The actual state observed at the time of the last action |
| `last_action` | S | The last action performed |
| `last_action_at` | S | When that happened (RFC3339) |
| `last_error` | S | The last error |
| `last_error_at` | S | When that happened (RFC3339) |
| `transitioning_since` | S | When the ongoing transition started (RFC3339). Unlike the other attributes this is not a snapshot: it disappears once the transition resolves |

`<type>#<ref>` is the identifier produced by `model.Resource.ID()`, which `internal/aws/tagging` derives from the ARN. The form of `ref` per type is given below.

| Type | `ref` form | Example `pk` |
|---|---|---|
| `rds-instance` | DB instance identifier | `status#rds-instance#dev-db` |
| `rds-cluster` | Cluster identifier | `status#rds-cluster#dev-cluster` |
| `ecs-service` | `<cluster name>/<service name>` | `status#ecs-service#dev-cluster/api` |
| `ec2-instance` | Instance ID | `status#ec2-instance#i-0abc123` |

### Writing

Writes go through `UpdateItem` with `SET`. Because this is not a wholesale replacement by `PutItem`, a partial update can change some attributes and leave the rest.

The attributes to update are given by `state.StatusPatch`. Every field is a pointer: `nil` means leave that attribute alone, and `state.Set("")` means clear it. `internal/state` is the only package that knows the attribute names, and no path exists by which the application layer assembles a DynamoDB attribute name as a string.

### Deletion

The item for a resource that no longer matches a selector is not deleted automatically, and leaving it has no effect on behaviour. The only deletion is the orphan pruning that goes through the diagnosis; its criteria and scope are described in [overview.md](overview.md).

## `status#group#<name>` — per-group results

The attribute shape is identical to `status#<type>#<ref>`. Its subject is the processing of a group rather than an individual resource, and it records the failures that stem from that group's own configuration: an invalid cron or timezone, a discovery failure, a selector collision.

`"group"` is never used as a resource-type constant, so this does not collide with the `pk` space of real resources. It is deleted together with `group#` and `override#` when a group is deleted; the per-resource `status#` items are not.

Selector collisions are recorded here rather than on the resource side. Written to a shared item, the error clearing by the group that owns the resource and the error recording by the groups that lose the tie would alternate on the same item every cycle, and the notifications would flap.

## Read/write matrix

Which components may read and write each kind of item is collected below.

| Item | reconciler | `cheapskate-cli` / web console |
|---|---|---|
| `group#<name>` | read only | read/write, deleted when the group is deleted |
| `override#<name>` | read only | read/write, deleted on explicit clearing and when the group is deleted |
| `status#<type>#<ref>` | write only | read only, deletes orphans only |
| `status#group#<name>` | write only | read only, deletes when the group is deleted and for orphans only |

Three layers hold this separation in place. What each layer guarantees is given below.

| Layer | Guarantee |
|---|---|
| Types | The window onto `internal/state` is an interface declaring only what each consumer needs. `reconcile.Store` has no `PutGroup`/`PutOverride`, and `groups.Store` and `doctor.Store` have no `UpdateStatus` |
| Code | As a result, neither a path from the reconciler that writes configuration nor a path from the CLI or web console that writes status will compile |
| IAM | The reconciler's execution role is granted no `dynamodb:PutItem`, and its `UpdateItem` is confined to `status#*` by a `dynamodb:LeadingKeys` condition. The CLI and web console roles are granted no `UpdateItem` |

## Access patterns

With no sort key and no GSI, fetching every attribute of one group would take three `GetItem` calls. To avoid that, any operation that reads the whole table is completed in a single `Scan` (with pagination), telling the kinds apart by `pk` prefix and joining them in memory by group name. Operating on one group alone uses `GetItem` and `PutItem`.
