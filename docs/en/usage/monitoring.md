# Logs, metrics, and notifications

This document explains how to inspect reconciler results, resource errors, and successful operations.

## Configuration and current-state inspection

Use CLI show or the web console's group detail to inspect stored configuration and current AWS state. For ECS, config represents tags and live represents AWS observations. Inspect live_error when a resource cannot be observed.

A successful show command does not mean every resource has reached its requested state. For inspection, use:

```console
cheapskate-cli -table cheapskate -output json show --group dev
```

## Structured logs

The reconciler and web console emit structured logs to stderr. Principal reconciler records are:

| Record | Content |
| --- | --- |
| summary | Resources inspected, successful operations, and group/resource error counts |
| action | Successful start, stop, or ECS configuration repair |
| group-error | Group configuration failure |
| resource-error | Resource inspection or operation failure |

Records include group, resource_id, desired, action, and error where applicable. Logs provide the ongoing operational record; DynamoDB stores no operation or error history.

A cycle that completes group reads and resource discovery ends with summary. A global read failure returns a Lambda error without summary. Individual group and resource failures do not fail the Lambda invocation, so inspect summary error counts as well.

## CloudWatch metrics

METRICS_ENABLED=true enables custom metrics in METRICS_NAMESPACE using Embedded Metric Format. Metrics are disabled by default. Values are:

| Metric | Value |
| --- | --- |
| ReconciledResources | Unique resources that reached inspection |
| ReconcileActions | Successful operations |
| ReconcileErrors | Group and resource errors |
| ReconcileAborted | 1 for a globally aborted cycle, otherwise 0 |

Successful global reads produce all four metrics. Failed reads produce only ReconcileAborted=1. All units are Count and there are no dimensions.

ReconcileErrors and ReconcileAborted can drive error detection. Provision CloudWatch Logs, alarms, dashboards, and retention separately.

## SNS notifications

NOTIFICATION_TOPIC_ARN enables notifications after successful starts, stops, or ECS configuration repairs. Success means the configuration request was accepted, not that tasks or instances completed their transitions.

A failed publish is retried once in the same invocation. If that also fails, the failure is logged without reversing the successful operation. Error and recovery notifications are not sent. Overlap and retries can duplicate notifications; response loss can also cause missing notifications.

Provision an SNS topic and subscriptions separately and grant sns:Publish to the reconciler. For variables, see [Runtime configuration](config.md#reconciler).
