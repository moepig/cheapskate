# Reconcile boundaries

One invocation takes two in-memory snapshots before it touches a resource: a strongly consistent Query of every group and every page of fixed-tag discovery. Failure of either global read returns an error with no resource Describe, Start, or Stop calls.

Each valid resource is then processed independently. The adapter describes it, transitional observations are skipped, and a stable mismatch receives Start or Stop. One resource failure increments the summary error count but does not stop later resources.

No action history or checkpoint is persisted. Overlapping invocations can repeat an absolute-state operation or use different configuration snapshots. Tests therefore assert eventual convergence in a later cycle rather than exactly-once delivery.

An action notification occurs after the successful resource call and receives at most two Publish attempts. Notification failure is logged but does not reverse the resource call or stop the cycle.
