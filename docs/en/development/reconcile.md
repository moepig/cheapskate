# Reconcile implementation contracts

This document defines read boundaries, failure isolation, overlapping invocation behavior, and the contracts verified by tests.

## Read boundary

Each invocation takes in-memory snapshots of all groups and discovered resources before resource processing. Group reads use a strongly consistent Query of the DynamoDB CONFIG partition. Discovery follows every Resource Groups Tagging API GetResources page for supported resources with the cheapskate:group tag, then verifies RDS DB cluster identities and membership tags through the RDS read API.

A global read, ARN parsing, or RDS cluster resolution failure returns an error without calling adapter Describe, Start, or Stop. Duplicate ARNs use the last complete tag set. RDS clusters use DBClusterIdentifier, DBClusterArn, and TagList from the RDS response, merging name-based and resource-ID-based discovery results under the same ARN. Each resource is processed once per cycle.

When an RDS query succeeds but the cluster or membership tag is absent, discovery logs the original ARN and reason and excludes the candidate. Identity mappings last for one discovery call. Clusters in disabled groups still receive discovery-time verification.

## Per-resource processing

Valid resources are processed in ARN order. Disabled groups skip Describe. Observation is compared with the group request; running/stopped mismatches and NeedsStart or NeedsStop select operations. Transitioning and not-found observations are skipped.

Group and resource failures are collected in Summary while later resources continue. For API order and adapter decisions, see [Resource operation sequences](resource_sequence.md).

## Stateless reapplication

Action history, checkpoints, and startup phases are not stored. ECS Start rereads the service and scalable target to select a branch; Stop rereads the scalable target. Group configuration and tags come from the cycle's initial snapshots.

Overlapping invocations can repeat absolute-state operations and apply different snapshots. Tests verify convergence in later cycles after older invocations finish, configuration stabilizes, and APIs succeed. They do not assume exactly-once operations.

## Notification boundary

Successful resource calls are followed by at most two SNS Publish attempts. Notification failure is logged without reversing the resource operation or stopping the cycle. If observation after a lost response shows no further changes are needed, the operation and notification are not replayed.

## Verification scope

Unit tests cover global read failures before processing, isolated resource errors, failure at each API boundary, response loss, tag changes, and suppression of operations after convergence. ECS tests also cover asynchronous task-count changes and Scheduled Scaling changes to normal bounds.

See [reconcile tests](../../../internal/app/reconcile/reconcile_test.go), [ECS tests](../../../internal/aws/compute/ecs_test.go), and [Scheduled Scaling tests](../../../internal/aws/compute/ecs_scheduled_scaling_test.go).
