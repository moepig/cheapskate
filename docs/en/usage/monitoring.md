# Logs, metrics, and notifications

This document explains how to inspect reconciler results, resource errors, and successful operations.

## Configuration and current-state inspection

For steps to compare saved configuration with current AWS state, see [Daily operations](operations.md#inspecting-configuration-and-current-state). The output fields are defined in [CLI output formats](cli.md#output-formats).

## Structured logs

The reconciler and web console emit structured logs to stderr. Logs provide the ongoing operational record; DynamoDB stores no operation or error history.

### Cycle results

A cycle that completes group reads and resource discovery emits summary after resource processing. Individual group and resource failures do not fail the Lambda invocation, so inspect summary error counts as well.

A global read failure returns a Lambda error without summary.

### Log records

Principal reconciler records are:

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
| notify-action-started / notify-action-finished | debug | Start and end of notification attempts |
| resource-discovered | debug | Discovered resource's group, resource_id, and resource_arn |
| resource-discovery-skipped | warn | Excluded RDS cluster candidate's resource_arn and reason |
| summary | info | Resource/action counts and group/resource error counts |
| action-started | info | Resource and state immediately before an action request |
| action-finished | info | Action outcome, desired state, and pre-action observed state |
| resource-unchanged | info | Desired/observed states and reason for no changes |
| resource-skipped | info | Resource, state, and reason for skipping |
| group-error | error | Group configuration failure |
| resource-error | error | Individual resource inspection or action failure |

Action records cover starts, stops, and ECS configuration repairs. The summary reports counts of inspected resources, successful actions, and group/resource errors.

Set LOG_LEVEL=debug to also emit debug records. Each operation's completion includes success and error on failure. Notifications include attempt for each publish attempt.

Discovery completion includes the resources count, even when zero. resource-discovered records appear in ARN order and include resources excluded from processing because of invalid or disabled groups.

The reason for resource-discovery-skipped is not-found or membership-tag-missing.

### Resource state and action results

Resource info records are emitted as processing progresses. Principal action-result fields are:

| Field | Content |
| --- | --- |
| group | Resource's group |
| resource_id / resource_arn | Target resource identifier / ARN |
| desired | Desired state: running or stopped; empty for disabled |
| action | Requested operation: start or stop |
| observed | Decision state; null for disabled resources |
| aws_state | AWS status or ECS task counts and scaling settings |
| reason | Reason for leaving a resource unchanged or skipping it |
| success | Whether the action request succeeded |
| error | Action request error |

observed classifies AWS observations as running, stopped, transitioning, or not-found. aws_state contains the underlying status or task counts and scaling configuration. Both reflect the values at the time of the decision.

success indicates request acceptance rather than transition completion. action-finished records the action request's outcome without reading state again for logging. Inspect the resulting state in the next cycle, with CLI show, or in the web console.

For resource-unchanged, reason=desired-state-matched means the observed state matches the desired state and no configuration repair is needed. For resource-skipped, reason is disabled, transitioning, or not-found.

Identify the AWS service and resource type from resource_id or resource_arn.

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

Provision an SNS topic and subscriptions separately and grant sns:Publish to the reconciler. For variables, see [Runtime configuration](config.md#reconciler).

NOTIFICATION_TOPIC_ARN enables notifications after successful starts, stops, or ECS configuration repairs. Success means the configuration request was accepted, not that tasks or instances completed their transitions.

A failed publish is retried once in the same invocation. If that also fails, the failure is logged without reversing the successful operation. Error and recovery notifications are not sent. Overlap and retries can duplicate notifications; response loss can also cause missing notifications.
