# Local emulation architecture

Local development and testing run against Floci, a local AWS emulator.

## How the connection is switched

Switching the endpoint is done through `AWS_ENDPOINT_URL` alone, which the AWS SDK interprets natively. No production code branches on the endpoint or carries a hook for tests.

## Floci

Floci is a LocalStack Community-compatible emulator, adopted after LocalStack Community began requiring an auth token and froze its updates. The connection details are given below.

| Item | Value |
|---|---|
| Endpoint | `http://localhost:4566` |
| Health check | `/_localstack/health` |
| docker socket | Mounted, because the RDS/ECS emulation starts real containers |

There are two ways to start it, both offering the same endpoint. What starts each, and what each is for, are given below.

| Route | Started by | Used for |
|---|---|---|
| `compose.yaml` | docker compose | Running the components from source |
| testcontainers-go | The test binary | Integration tests and image tests |

### Container lifecycle

The connection helpers live in `internal/devtools/emutest`. testcontainers-go starts exactly one container under a fixed name, and every test binary running alongside — and every later run — reuses it. Everything the tests create is namespaced, so sharing is safe.

The Ryuk reaper is disabled. `go test ./...` runs a separate binary per package while testcontainers keeps one session across all of them, and Ryuk deletes that session's containers the moment the client connection count reaches zero. The first binary to finish would therefore delete the emulator the still-running binaries are using. Deletion is done explicitly instead.

### What is not covered, and the substitutes

The APIs the emulator does not reproduce, and what stands in for them, are collected below.

| Area | Substitute |
|---|---|
| The RDS Stop/Start APIs | `tests/system` uses the real client for Describe only and replaces Stop/Start with a spy. The real calls are verified in acceptance testing |
| `tag:GetResources` (partial) | `tests/system` replaces discovery with a stub. All it does against Floci is read and write DynamoDB |
| The Application Auto Scaling API | Starting and stopping ECS always fails on Floci. Unit tests use gomock, and the real calls are verified in acceptance testing |
| Transitional states (starting/stopping) | Verified by unit tests with a fake that returns transitional states |

## The state table

The Go definition of the schema (`pk` hash key + `expires_at` TTL) lives in `internal/state` and nowhere else. Locally, `cmd/dev-bootstrap` creates the table; it is idempotent and is not in the Lambda images. The production table is created with the same schema by whatever means the hosting side uses.

## Dummy resources

`internal/devtools/devseed` creates an ECS cluster and services, including ones matching the selector, ones carrying parameter tags, and ones matching nothing, so the group page and `show` can be inspected.

There are no dummy RDS/EC2 resources. Support for the Tagging API is limited as well, so types other than ECS yield either an empty resource list or a discovery error.

## Running the Lambda runtime locally

Running a built image as a Lambda uses the Runtime Interface Emulator bundled in the base image. What the RIE stands in for is the Lambda execution environment itself, and it is not identical to production. What it covers and what still differs are described in [on_lambda.md](on_lambda.md).

testcontainers-go starts and cleans up the container. The emulator and a disposable state table come up alongside the tests through `emutest`, and the container reaches the emulator through `host.docker.internal`. Either way of starting the emulator works, as long as the port is published on the host.

No platform is specified for the build. The RIE runs the container on the host's docker, so a build for the host's own architecture is the correct one.

### Why the image build is delegated to the docker CLI

testcontainers-go builds images through the old `/build` API, which cannot interpret this BuildKit-based `Dockerfile`. Adding an option that selects BuildKit does not help either, because the docker CLI is what establishes the session, and the client library alone fails at it.
