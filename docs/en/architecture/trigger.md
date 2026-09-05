# Invocation

A five-minute schedule is the only automatic invocation path. Manual invocation is optional. The handler ignores payload content and performs a full reconcile for every invocation.

Async delivery may discard an invocation after throttling or retry exhaustion. The next successful periodic invocation processes the current configuration. Normal additional delay is therefore at most the five-minute interval plus one cycle duration; continuous failure has no bounded convergence time.
