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

| Record | Level | Content |
| --- | --- | --- |
| reconcile-started / reconcile-finished | debug | Start and end of the full cycle |
| load-groups-started / load-groups-finished | debug | Start and end of group configuration reads |
| resolve-desired-started / resolve-desired-finished | debug | Start and end of desired-state resolution |
| discover-resources-started / discover-resources-finished | debug | Start and end of resource discovery |
| reconcile-resources-started / reconcile-resources-finished | debug | Start and end of overall resource processing |
| describe-resource-started / describe-resource-finished | debug | Start and end of state inspection before an action |
| decide-action-started / decide-action-finished | debug | Start and end of action decisions |
| apply-action-started / apply-action-finished | debug | Start and end of action requests |
| describe-resource-after-action-started / describe-resource-after-action-finished | debug | Start and end of state inspection after an action |
| notify-action-started / notify-action-finished | debug | Start and end of notification attempts |
| resource-discovered | debug | Discovered resource's group, resource_id, resource_arn, and resource_type |
| summary | info | Resources inspected, successful operations, and group/resource error counts |
| action-started | info | Resource and state immediately before requesting a start, stop, or ECS configuration repair |
| action-finished | info | Action outcome, desired state, and observed states before and after the action |
| resource-unchanged | info | Desired and observed states of a resource requiring no changes |
| resource-skipped | info | Resource and state when processing is skipped for disabled, transitioning, or not-found |
| group-error | error | Group configuration failure |
| resource-error | error | State inspection before or after an action, or operation failure |

Set LOG_LEVEL=debug to also emit debug records. Each operation's completion includes success and error on failure. Notifications include attempt for each publish attempt. Discovery completion includes the resources count, even when zero. resource-discovered records appear in ARN order and include resources excluded from processing because of invalid or disabled groups.

Resource info records are emitted as processing progresses. Principal action-result fields are:

| Field | Content |
| --- | --- |
| group | Resource's group |
| resource_id / resource_arn | Target resource identifier / ARN |
| service | AWS service: ec2, ecs, or rds |
| resource_type | Resource type: ec2-instance, ecs-service, rds-instance, or rds-cluster |
| desired | Desired state: running or stopped; empty for disabled |
| action | Requested operation: start or stop |
| observed_before / observed_after | Observed state before / after the action |
| detail_before / detail_after | AWS status or ECS counts and scaling configuration before / after the action |
| success | Whether the action request succeeded; does not indicate transition completion |
| error | Action request error |
| observation_error | State inspection error after the action |

After every attempted action, state is read once more and action-finished records both observations. observed_after reflects that read without waiting for the desired state to be reached. An ECS repair can leave both states running while detail_before and detail_after show configuration changes.

If the post-action inspection fails, observed_after is null. A successful action request still counts as successful and triggers SNS notification; the inspection failure counts as an error. Resources requiring no action use the same observation for observed_before and observed_after. Disabled resources are not inspected, so both values are null.

Logs provide the ongoing operational record; DynamoDB stores no operation or error history.

A cycle that completes group reads and resource discovery emits summary after resource processing. A global read failure returns a Lambda error without summary. Individual group and resource failures do not fail the Lambda invocation, so inspect summary error counts as well.

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
