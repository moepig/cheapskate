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

Unknown attributes and malformed attribute types invalidate the group. Expired overrides remain valid stored data and are ignored during desired-state resolution.

All group enumeration uses a strongly consistent Query on `CONFIG`. Configuration changes first use a strongly consistent `GetItem`, validate the complete result, and then issue an operation-specific conditional write. Attribute-level updates preserve concurrent changes to unrelated attributes. A conditional failure is reported as a conflict and is not retried automatically.
