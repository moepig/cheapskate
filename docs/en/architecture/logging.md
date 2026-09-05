# Logging

The reconciler and web console emit structured logs to stderr. Reconcile records use `group`, `resource_id`, `desired`, `observed`, `action`, and `error` where applicable.

Every cycle ends with a `summary` record containing the number of resources that reached Describe, successful actions, and group or resource errors. Global configuration and discovery failures are returned as Lambda errors.

Successful Start and Stop operations emit an `action` record. Failures emit `group-error` or `resource-error`. Logs are the durable operational record because no action or error history is stored in DynamoDB.
