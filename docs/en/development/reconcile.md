# Reconcile boundaries

One invocation takes two in-memory snapshots before it touches a resource: a strongly consistent Query of every group and every page of fixed-tag discovery. Failure of either global read returns an error with no resource Describe, Start, or Stop calls.

Each valid resource is then processed independently. The adapter describes it, transitioning and not-found observations are skipped, and a running/stopped mismatch receives Start or Stop. One resource failure increments the summary error count but does not stop later resources.

The ECS adapter reports stopped only when desired, running, and pending counts are all zero, and running otherwise. A stopped request applies bounds of 0/0 and ScheduledScalingSuspended=true, including repairs for already stopped services. A running request repairs incomplete startup, a mismatch between the suspension request and the AWS flag, and bounds differences while the request is true. During normal operation with a false request, Scheduled Scaling changes to bounds are retained.

ECS initialization resumes from a stopped service, bounds of 0/0, or a suspended positive fixed range that differs from the normal tag bounds. Scheduled Scaling stays suspended until capacity initialization succeeds; the final update applies normal bounds and the tag request. Reapplying a running suspension setting does not reset desired count to startup capacity. If normal bounds equal startup capacity, a remaining flag mismatch alone can be repaired. A lost final response requires no additional operation if settings have already converged.

No action history or checkpoint is persisted. Overlapping invocations can repeat an absolute-state operation or use different configuration snapshots. Tests therefore assert eventual convergence in a later cycle rather than exactly-once delivery.

An action notification occurs after the successful resource call and receives at most two Publish attempts. Notification failure is logged but does not reverse the resource call or stop the cycle.

For the common cycle and AWS API call order for each resource, see [Resource operation sequences](resource_sequence.md).
