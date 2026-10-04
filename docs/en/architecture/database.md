# DynamoDB data model

The state table has a String partition key named `pk` and a String sort key named `sk`. It has no secondary index and no TTL configuration.

Only group items are stored. One group uses `pk=CONFIG` and `sk=GROUP#<name>`.

| Attribute | Type | Meaning |
| --- | --- | --- |
| `pk` | S | `CONFIG` |
| `sk` | S | `GROUP#<name>` |
| `start_cron` | S | Start schedule; present together with `stop_cron` |
| `stop_cron` | S | Stop schedule; present together with `start_cron` |
| `override` | S | `running`, `stopped`, or `disabled` |
| `override_expires_at` | N | Optional Unix time in seconds |
| `ecs_max_count` | N | Optional positive int32 limit on desired count and scaling maximum for each ECS service in the group |

Unknown attributes and malformed attribute types invalidate the group. Expired overrides remain valid stored data and are ignored during desired-state resolution.

`ecs_max_count` applies to each ECS service, not the group's total task count. ECS Start requires this attribute and rejects missing limits or tag values above the limit before any modifying API call. Stop does not apply this limit. Zero, negative, fractional, out-of-int32-range, and non-Number values invalidate the stored configuration.

All group enumeration uses a strongly consistent Query on `CONFIG`. Configuration changes first use a strongly consistent GetItem and validate attribute decoding before an operation-specific conditional write. Schedule, override, and clear-override validate the complete resulting group; remove does not validate cron expressions or cross-attribute invariants. Attribute-level updates preserve concurrent changes to unrelated attributes. A conditional failure is reported as a conflict and is not retried automatically.

Creation requires an absent item. Existing schedule updates, indefinite override updates, and deletion require an existing item. Timed overrides and clear-override also require both cron attributes to exist. Conditions do not compare values or revisions, so writes to the same attributes do not conflict; the last-applied values remain.
