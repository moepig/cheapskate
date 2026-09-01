# DynamoDB table structure

The key layout and item shapes of the single DynamoDB table that holds the state. The schema, the key layout, and the Go representation of the items are all closed inside `internal/state`; `internal/core/model` holds domain types only and knows nothing of the storage format.

## Table definition

What the table is required to provide is given below.

| Item | Value |
|---|---|
| Partition key | `pk` (String) |
| Sort key | `sk` (String) |
| GSI / LSI | none |
| TTL | attribute `expires_at` (Number, epoch seconds). Overrides, status items, and the reconcile lease carry it |

## The item kinds

Five kinds of item are stored. What each key value represents, along with its writers and readers, is collected below.

| Item | `pk` value and meaning | `sk` value and meaning | Writer | Reader |
|---|---|---|---|---|
| Group configuration | `CONFIG` — the partition containing group configuration inputs | `GROUP#<name>` — durable configuration for the named group | `cheapskate-cli` / web console | reconciler, CLI, web console |
| Override | `CONFIG` — the partition containing group configuration inputs | `OVERRIDE#<name>` — a time-limited override for the named group | Same as above | Same as above |
| Status (per resource) | `STATUS#<type>#<ref>` — reconcile results for the identified AWS resource | `CURRENT` — the latest status for that resource | reconciler | CLI, web console |
| Status (per group) | `STATUS#group#<name>` — processing results for the named group | `CURRENT` — the latest status for that group | reconciler | CLI, web console |
| Reconcile lease | `LOCK` — the partition for reconcile exclusion state | `RECONCILE` — the global full-reconcile lease | reconciler | reconciler |

[`itemKey`](../../../internal/state/items.go#L14) represents these key combinations. [`model.ResourceType`](../../../internal/core/model/resource.go#L10) defines the `<type>` variants in `STATUS#<type>#<ref>`; the per-resource results section below gives the corresponding `<ref>` forms. Fixed key values and prefixes are case-sensitive.

## `CONFIG` / `GROUP#<name>` — group configuration

The domain representation is `model.GroupSpec`. Storage puts the group name in `sk`, and reading restores it to the `Name` field. The attributes are given below.

| Attribute | Type | Meaning |
|---|---|---|
| `pk` | S | `CONFIG` |
| `sk` | S | `GROUP#<name>` |
| `mode` | S | `pinned` \| `schedule` \| `disabled`. Unset is treated as `disabled` |
| `desired` | S | Meaningful only with `mode: pinned`. `running` \| `stopped` |
| `start_cron` / `stop_cron` | S | Only with `mode: schedule`. Five-field cron expressions |
| `timezone` | S | IANA timezone name. Unset falls back to the reconciler's `DEFAULT_TIMEZONE` |
| `tag_key` / `tag_value` | S | The selector's tag condition |
| `types` | SS | The resource types the selector targets |

A group name matches `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`. Neither `#` nor `/` may appear: `#` is the `sk` separator, and `/` can collide with an ECS `ref`.

A group may be created with no selector (`tag_key`, `tag_value`, and `types` all empty), but setting `mode` to `pinned` or `schedule` requires one. An empty StringSet cannot be represented in DynamoDB, so with no selector the `types` attribute is omitted entirely.

## `CONFIG` / `OVERRIDE#<name>` — a time-limited override

The domain representation is `model.Override`. The attributes are given below.

| Attribute | Type | Meaning |
|---|---|---|
| `pk` | S | `CONFIG` |
| `sk` | S | `OVERRIDE#<name>` |
| `desired` | S | `running` \| `stopped` |
| `expires_at` | N | epoch seconds. The attribute DynamoDB TTL acts on |

The reading side ignores an item with `expires_at <= now` as expired, because TTL deletion is asynchronous and can lag by up to 48 hours. The TTL itself exists only to tidy away the leftover items.

## `STATUS#<type>#<ref>` / `CURRENT` — per-resource results

The domain representation is `model.Status`. The values are a snapshot taken when the last action or error occurred, not a live state. The attributes are given below.

| Attribute | Type | Meaning |
|---|---|---|
| `pk` | S | `STATUS#<resource ID>` |
| `sk` | S | `CURRENT` |
| `observed_state` | S | The actual state observed at the time of the last action |
| `last_action` | S | The last action performed |
| `last_desired` | S | The desired state targeted by the last action |
| `last_action_at` | S | When that happened (RFC3339) |
| `last_error` | S | The last error |
| `last_error_at` | S | When that happened (RFC3339) |
| `transitioning_since` | S | When the ongoing transition started (RFC3339). Unlike the other attributes this is not a snapshot: it disappears once the transition resolves |
| `pending_operation_id` | S | The operation ID written before an AWS action. Non-empty means completion is awaiting confirmation |
| `pending_group` | S | The group that owned the resource when the operation started |
| `pending_config_hash` | S | SHA-256 of the effective group configuration, override, and resolved desired state when the operation started |
| `pending_action` / `pending_desired` / `pending_observed` | S | The pending action, its target, and the observation before it ran |
| `pending_started_at` | S | When the pending operation started (RFC3339) |
| `expires_at` | N | Epoch seconds at the last status update plus `STATUS_RETENTION_DAYS`. The attribute DynamoDB TTL acts on |

Status is latest-only, not a history. Every update extends `expires_at`; DynamoDB TTL deletes status items that stop receiving updates after the retention period. Before an AWS action, the pending attributes are written conditionally. Afterwards, the same operation ID is required to advance the item to the last-action state. If Lambda stops between these steps, the next invocation confirms completion from the AWS observation instead of repeating the action. If tag membership or configuration changes while an operation is pending, `pending_group` remains the completion-notification destination and the current configuration takes effect in the following cycle. Notifications are not stored in status. See [Reconcile persistence boundaries](../development/reconcile.md) for the persistence and notification boundaries.

Each status attribute is decoded independently. A malformed audit attribute leaves the valid attributes and its decode error available, while group and override resolution and AWS actions continue. A malformed `pending_` attribute fails that resource closed because the reconciler cannot determine whether the AWS action already ran. Decode errors are shown by `cheapskate-cli`, the web console, and `doctor`.

`<type>#<ref>` is the identifier produced by `model.Resource.ID()`, which `internal/aws/tagging` derives from the ARN. The form of `ref` per type is given below.

| Type | `ref` form | Example `pk` |
|---|---|---|
| `rds-instance` | DB instance identifier | `STATUS#rds-instance#dev-db` |
| `rds-cluster` | Cluster identifier | `STATUS#rds-cluster#dev-cluster` |
| `ecs-service` | `<cluster name>/<service name>` | `STATUS#ecs-service#dev-cluster/api` |
| `ec2-instance` | Instance ID | `STATUS#ec2-instance#i-0abc123` |

### Writing

Writes go through `UpdateItem` with `SET`. Because this is not a wholesale replacement by `PutItem`, a partial update can change some attributes and leave the rest.

The attributes to update are given by `state.StatusPatch`. Every field is a pointer: `nil` means leave that attribute alone, and `state.Set("")` means clear it. `internal/state` is the only package that knows the attribute names, and no path exists by which the application layer assembles a DynamoDB attribute name as a string.

### Deletion

An item that stops receiving updates, for example because its resource no longer matches a selector, becomes eligible for DynamoDB TTL deletion after the retention period. Deletion is asynchronous and the item can remain for up to 48 hours after expiry. To remove it before the retention period elapses, use the diagnosis-driven orphan pruning described in [overview.md](overview.md).

An existing status item with no `expires_at` is not eligible for TTL deletion. The reconciler assigns an expiry when it updates the item; use `doctor --prune` for existing orphaned status items that receive no further updates.

## `STATUS#group#<name>` / `CURRENT` — per-group results

The attribute shape is identical to `STATUS#<type>#<ref>` / `CURRENT`. Its subject is the processing of a group rather than an individual resource, and it records the failures that stem from that group's own configuration: an invalid cron or timezone, a discovery failure, a selector collision. A group-status decode error is distinct from a group-configuration error and does not stop resource reconciliation.

`"group"` is never used as a resource-type constant, so this does not collide with the `pk` space of real resources. It is deleted together with `CONFIG` / `GROUP#<name>` and `CONFIG` / `OVERRIDE#<name>` when a group is deleted. Per-resource `STATUS#<type>#<ref>` / `CURRENT` items are not.

Selector collisions are recorded here rather than on the resource side. Written to a shared item, the error clearing by the group that owns the resource and the error recording by the groups that lose the tie would alternate on the same item every cycle, and the notifications would flap.

## Reconcile lease

`LOCK` / `RECONCILE` is a lease preventing two full reconciles from running at once. It holds an invocation-specific `owner` and an `expires_at` timestamp. Acquisition is an `UpdateItem` conditioned on the item being absent or expired; release is a `DeleteItem` conditioned on matching the owner. An invocation that cannot acquire it performs no configuration read or AWS call and returns success.

## Read/write matrix

Which components may read and write each kind of item is collected below.

| Item | reconciler | `cheapskate-cli` / web console |
|---|---|---|
| Group configuration | read only | read/write, deleted when the group is deleted |
| Override | read only | read/write, deleted on explicit clearing and when the group is deleted |
| Resource status | read/write | read only, deletes orphans only |
| Group status | read/write | read only, deletes when the group is deleted and for orphans only |
| Reconcile lease | read/write/delete | no access |

Three layers hold this separation in place. What each layer guarantees is given below.

| Layer | Guarantee |
|---|---|
| Types | The window onto `internal/state` is an interface declaring only what each consumer needs. `reconcile.Store` has no `PutGroup`/`PutOverride`, and `groups.Store` and `doctor.Store` have no `UpdateStatus` |
| Code | As a result, neither a path from the reconciler that writes configuration nor a path from the CLI or web console that writes status will compile |
| IAM | Reconciler `UpdateItem` is confined to `STATUS#*` and `LOCK`, and `DeleteItem` to `LOCK`. The CLI and web console can alter only `CONFIG` |

## Access patterns

Normal lists and reconciles use a strongly consistent `Query` on `pk=CONFIG`, then strongly consistent `BatchGetItem` requests—at most 100 keys each—for only the statuses they need. A full-table `Scan` is reserved for `doctor`. Displaying one group reads its configuration, override, and group status in one `BatchGetItem`.

Group configuration changes are attribute-level `UpdateItem` requests. Concurrent changes to different attributes are retained; for the same attribute, the last write wins. No read-followed-by-wholesale-replacement is used.
