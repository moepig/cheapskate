# Logging

The reconciler and web console emit structured logs to stderr. Reconcile records use `group`, `resource_id`, `desired`, `action`, and `error` where applicable.

Every cycle that completes both global reads ends with a `summary` record containing the number of resources that reached Describe, successful actions, and group or resource errors. Global configuration and discovery failures return Lambda errors without a summary log record. Group and resource failures do not fail the Lambda invocation.

Successful Start and Stop operations emit an `action` record. Failures emit `group-error` or `resource-error`. Logs are the durable operational record because no action or error history is stored in DynamoDB.
