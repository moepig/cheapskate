# Testing

The main test targets are:

```console
make unit
make integration
make test
make image-test
make lint
```

Unit tests cover group invariants, cron reachability, desired-state resolution, pagination, conditional writes, adapter validation, reconciliation barriers and isolation, CLI output, and web security behavior.

Integration tests use the local AWS emulator for real DynamoDB expressions and the full schedule/override lifecycle. The system test covers schedule Stop, timed running override, expiry back to the schedule, disabled suppression, a stale-write conflict, and concurrent changes to separate attributes.

Image tests build both container images. The reconciler receives arbitrary JSON payloads and completes a full cycle. The web test validates the Lambda Web Adapter path and request metadata handling. Startup tests cover UTC defaults and invalid `DEFAULT_TIMEZONE` values.

AWS SDK boundaries use generated mocks; application ports use hand-written doubles. See [Mocks](mock.md).
