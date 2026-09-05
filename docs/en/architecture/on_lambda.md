# Lambda deployment

The reconciler image runs as a Lambda handler. The web console image runs an HTTP server behind Lambda Web Adapter and an API Gateway integration.

The reconciler Lambda timeout must accommodate every configuration item, every tagged-resource page, and all Describe and change calls in one invocation. Lambda allows a maximum timeout of 900 seconds. cheapskate has no checkpoint or split-execution mechanism.

Reserved concurrency of one is an optional cost optimization. Correctness does not depend on excluding concurrent invocations.
