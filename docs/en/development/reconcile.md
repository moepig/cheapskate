# Reconcile persistence boundaries

The reconciler cannot place an AWS action and a DynamoDB write in one transaction. It therefore moves status through operation intent and operation result, allowing a later Lambda invocation to resolve the AWS action after an interruption. Notifications are outside the status transition.

## Persisting AWS actions

Processing for each resource proceeds in this order.

1. Record an operation intent only when `pending_operation_id` is empty.
2. Perform the AWS action.
3. Conditionally and atomically replace that intent with the completion record, using the same operation ID.
4. Publish a notification containing the action time, with at most two attempts.

If execution ends after a successful AWS action but before its completion record, `pending_operation_id` remains. The next cycle resolves the result from the observed AWS state and does not perform the same AWS action again.

## Notification delivery boundary

Each notification gets at most two Publish attempts within the same processing run. After two failures, the reconciler abandons it without leaving notification retry state in status. Notification failure is not a reconcile error and does not stop a later AWS action.

The `at` field is the time of the action, error, or recovery being reported, not the notification-send time. Both attempts retain the same `at`. If different notifications arrive out of order, their `at` values establish their ordering. The `operation_id` in an action notification identifies duplicates for the same operation.

Because notifications are not durable, one can be lost if Lambda ends after recording completion but before Publish. Durable retry would require outbox items keyed by operation ID and a DynamoDB transaction that commits the AWS action completion record and the outbox item together.
