# Testing

This document covers the test layers, what each layer covers and how it runs, and where the test code lives.

The `make` targets that serve as entry points are given below.

```console
make unit         # AWS-free unit tests (go test ./...)
make integration  # tests tagged `integration`; needs Docker
make test         # both
make image-test   # tests tagged `image`; drives the built container images from outside
make lint         # gofmt + go vet (with the integration and image tags) + cross-compilation
```

## Test layers

The tests fall into four layers by the granularity of their subject. What each layer covers and how it runs are collected below.

| Layer | Subject | Means |
|---|---|---|
| Unit (`make unit`) | Desired-state resolution, configuration validation, convergence decisions, restoring ECS values from tags, ARN parsing | Time is injected as an argument. AWS clients are narrow interfaces plus mocks |
| Integration (`make integration`) | The store's real DynamoDB calls and each CLI command, plus the reconcile flow wired to real adapters | The local emulator |
| Image (`make image-test`) | That a built image starts and responds as a Lambda runtime, handling of real event payloads, responses through the Lambda Web Adapter | The Runtime Interface Emulator |
| Acceptance (real AWS) | The 7-day auto-start, transition timing, the RDS Stop/Start APIs, real Auto Scaling behaviour | A deployment to a dev account |

The acceptance layer stands apart because the emulator reproduces neither the RDS Stop/Start APIs nor the Application Auto Scaling API. For the full extent of what cannot be reproduced and the substitutes used in the lower layers, see [../architecture/emulation_local.md](../architecture/emulation_local.md).

## Where the tests live

If the subject is a Go package, the test sits next to that package. If the subject is something assembled, it goes under `tests/`. Whether a real emulator is involved is not a criterion.

The subject and build tag for each location are collected below.

| Location | Subject | Tag |
|---|---|---|
| `internal/**/*_test.go` | That package's contract. Integration tests against real DynamoDB live here too | none / `integration` |
| `tests/system/` | The reconcile loop wired to real adapters; the test assembles the wiring `internal/wire` does in production | `integration` |
| `tests/image/` | The built container images themselves | `image` |

Everything under `tests/` is a black-box test that imports nothing from its subject and belongs to no package. A `doc.go` in each directory (with no build tag) states what that directory is for.

## Integration tests

These run against the local AWS emulator. testcontainers-go starts it automatically, so nothing has to be prepared as long as Docker is running.

`make integration` runs both the per-package integration tests and `tests/system/`. Both need only the emulator and no image build.

The container is reused rather than discarded when the tests end. `make floci-down` deletes it, along with the RDS/ECS containers the emulator started.

## Image tests

`make image-test` is the only layer whose subject is a build artifact rather than a package. It builds both images, starts each on the Runtime Interface Emulator bundled in the base image, and invokes it over HTTP.

The Lambda handler, the JSON input/output contract, the `lambda.norpc` build tag, and the bundled Lambda Web Adapter are on the path in this layer alone. The RIE is a means rather than a subject, so the harness that holds it lives in `harness_test.go` and the checks themselves live in a file per image.

The state table is a disposable empty one in each case, which keeps the expected responses fixed.

The payloads sent to the reconciler and what is expected of them are as follows.

| Payload | Expectation |
|---|---|
| `{}` | A Summary with zero resources |
| Every file matching `internal/app/reconcile/testdata/rds-event-*.json` (exactly as EventBridge delivers them) | The Summary, plus one `event-received` line in the container log per fixture |
| `[]` (a payload that is not an object) | Not taken as an empty event; unmarshalling fails |

The payloads sent to webconsole and what is expected of them are as follows.

| Payload | Expectation |
|---|---|
| An API Gateway REST API (v1) `GET /` proxy event | An HTTP 200 proxy response, showing that the adapter started as an extension, turned the event into HTTP over the loopback, and turned the response back into the event's response format |
| The same event with a spoofed `x-amzn-request-context` added by the client | The `client` in the log comes from the event's `requestContext`, and the spoofed IP appears nowhere in the log |

The tag is separate from `integration` because the images have to be built first. Through `COPY . .` in the `Dockerfile`, a change to any single file in the repository invalidates the layer cache.

The image build is delegated to `docker build` rather than testcontainers-go on account of BuildKit. For the reasoning, see how the image build is handled in [../architecture/emulation_local.md](../architecture/emulation_local.md).

## Fixtures

The sample RDS events live in `internal/app/reconcile/testdata/` and double as the reference payloads for the EventBridge rule pattern. Validate changes with `aws events test-event-pattern`. `make image-test` globs that directory, so adding a fixture automatically runs it against the images too.

## Mocks

Assertions use testify. Test doubles come in two forms: generated at the AWS SDK boundary, and hand-written for the application-layer ports. For the criteria for choosing between them, the generation steps, and how to write a hand-written double, see [mock.md](mock.md).
