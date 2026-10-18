# Testing

This document covers automated test commands, verification scope, and limitations.

## Choose a test target

The main make targets for running tests are:

```console
make unit
make integration
make test
make image-test
make lint
```

| Target | Scope |
| --- | --- |
| `make unit` | Go unit tests |
| `make integration` | Integration tests using the emulator |
| `make test` | Unit and integration tests |
| `make image-test` | Startup and invocation of both images |
| `make lint` | fmt, vet, and CLI cross-compilation |

## Unit tests

Unit tests verify domain, state-store, adapter, reconciliation, CLI, and web behavior.

- Domain: group invariants, cron reachability, desired-state resolution
- State store: pagination and conditional writes
- Adapters: input and operation validation
- Reconciliation: no processing before all reads complete, error isolation
- CLI: output
- Web: security behavior

Aurora discovery tests verify that no resource observation, operation, or notification begins before all discovery reads succeed. They mix multiple clusters with their aliases, reject reuse of identity mappings across accounts or Regions, and inject failures after earlier clusters have resolved successfully.

Unit tests at AWS SDK boundaries use generated mocks; application ports use hand-written doubles. See [Mocks](mock.md).

## AWS SDK connection tests

SDK connection tests use the actual AWS SDK clients constructed by the composition root against a local HTTP server. They cover Tagging API JSON, RDS XML, signed requests, resource ID filters, pagination, and propagation of AccessDenied errors.

## Integration tests

Integration tests use the local AWS emulator for real DynamoDB expressions and the full schedule/override lifecycle.

The system test covers these schedule/override and concurrent-write conditions:

- Schedule Stop
- Timed running override
- Expiry back to the schedule
- Disabled suppression
- A stale-write conflict
- Concurrent changes to separate attributes

RDS integration tests create multiple Aurora clusters in Floci and delete them after the test.

The pinned [Floci 1.7.0 RDS implementation](https://github.com/floci-io/floci/blob/75c8cfc4d93b62063b6a435dff0fc77df180ff84/src/main/java/io/github/hectorvent/floci/services/rds/RdsQueryHandler.java) omits cluster TagList and does not support the db-cluster-resource-id filter. Integration tests verify exclusion of responses without verifiable membership tags and discovery failure for responses that do not uniquely identify a cluster. Successful AWS-format responses are covered by the local HTTP connection tests.

These tests do not connect to AWS or evaluate IAM policies themselves.

## Image tests

Image tests build and invoke both container images. The reconciler receives arbitrary JSON payloads and completes a full cycle. The web test validates the Lambda Web Adapter path and request metadata handling.

Startup tests verify the UTC time-zone default and handling of invalid `DEFAULT_TIMEZONE` values.
