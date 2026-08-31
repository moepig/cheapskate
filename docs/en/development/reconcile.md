# Reconcile persistence boundaries

The reconciler cannot place an AWS action and a DynamoDB write in one transaction. It therefore moves status through operation intent, operation result, and notification acknowledgement, allowing a later Lambda invocation to continue after an interruption.

## Why only one action notification is stored

Each resource status stores only one action notification awaiting retry, in `notification_pending`. This is not a reduced notification queue. It is a storage format derived from the invariant that operations on one resource are serialized.

Processing proceeds in this order.

1. Record an operation intent only when both `pending_operation_id` and `notification_pending` are empty.
2. Perform the AWS action.
3. Conditionally and atomically replace that intent with the completion record and `notification_pending`, using the same operation ID.
4. Publish the notification, then conditionally clear `notification_pending` using the same operation ID.
5. Do not begin another AWS action until the marker has been cleared.

Before subsequent processing of a resource, the reconciler retries any unacknowledged notification and stops processing that resource if either publishing or recording the acknowledgement fails. DynamoDB conditions also reject starting an operation while a notification is unacknowledged and reject overwriting the notification slot. A valid state transition therefore cannot produce a second unacknowledged notification, so one slot loses no information.

This storage format has the following benefits.

- Completion of an AWS action and transition to notification-pending state are committed with one update to one status item.
- The item remains bounded during a notification outage, with no separate notification ordering, retention, or queue-cleanup mechanism.
- A later operation cannot overtake one whose notification is unacknowledged, so receivers can process resource changes in operation order.

The corresponding trade-off is that a prolonged notification outage also pauses new AWS actions for that resource. This is intentional: it prevents an action notification from being lost.

Strictly speaking, `notification_pending` identifies a notification whose delivery has not been acknowledged, rather than one that has never been sent. If the process stops after Publish succeeds but before the acknowledgement is recorded, the next cycle resends the same `operation_id`. Delivery is therefore at-least-once, and receivers can use `operation_id` for deduplication.

## When to migrate to multiple entries

The single-slot invariant no longer holds if any of the following becomes necessary.

- Continue AWS actions on the same resource during a notification outage.
- Retain action-notification history in cheapskate.
- Track acknowledgement independently for multiple notification destinations.

In that case, migrate to outbox items keyed by operation ID instead of adding a list attribute to status. The action completion and outbox creation must be committed in a DynamoDB transaction, with ordering, retention, deduplication, and cleanup defined explicitly.
