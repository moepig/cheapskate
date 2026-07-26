# Testing

```console
make unit         # AWS-free unit tests (go test ./...)
make integration  # tests tagged `integration`; needs Docker (below)
make test         # both
make image-test   # tests tagged `image`; drives the built container images (below)
make lint         # gofmt + go vet (with the integration and image tags)
```

## Where a test goes

If the subject is a Go package, the test sits next to that package. If the subject is something assembled — a wired-up system, a built image — it goes under `tests/`. Whether it talks to a real emulator is not the criterion; that is a means, not a subject.

| Location | Subject | Tag |
|---|---|---|
| `internal/**/*_test.go` | that package's contract — including the `internal/state` and `internal/ui/cli` integration tests, whose subject is still one package's contract with a real DynamoDB behind it | none / `integration` |
| `tests/system/` | the reconcile loop wired to the real adapters (state, aws/compute, aws/sns) — the test assembles by hand what `internal/wire` assembles in production | `integration` |
| `tests/image/` | the built container image itself | `image` |

Nothing under `tests/` imports the thing it drives, so it belongs to no package inside it. Each directory's `doc.go` (no build tag) states its remit.

## Integration tests and the local emulator

Integration tests run against **Floci**, a local AWS emulator, started automatically via [testcontainers-go](https://golang.testcontainers.org/) — just run `make integration` (or `go test -tags integration ./...`) with Docker running. No `docker compose` step and no pre-existing container are required.

- Wiring lives in `internal/devtools/emutest`: the first test to need it starts a Floci container named `cheapskate-itest-floci`, and every other test — in that binary, in the other packages' binaries running alongside it, and in later runs — reuses that same container. Tests namespace the resources they create, so sharing is safe.
- The container is deliberately left running instead of being reaped: `go test ./...` runs one binary per package but all of them share a single testcontainers session, and Ryuk prunes that session's containers as soon as its client count drops to zero — the first binary to finish would take the emulator out from under the ones still running. Ryuk is therefore disabled for it; `make floci-down` removes the container (and the RDS/ECS containers Floci spawns).
- `make integration` picks up both the package-level integration tests under `internal/` and `tests/system/`: neither needs more than the emulator, and neither builds an image.
- Floci mounts the docker socket because its RDS/ECS emulation runs real containers.
- Floci's Resource Groups Tagging API (`tag:GetResources`) support is limited, so integration tests never rely on it for group-membership discovery: they inject a hand-written stub `discover.Discoverer` into `reconcile.Deps`/`ops` instead of the real `discover.TaggingDiscoverer`. Only DynamoDB (state table) reads/writes go through Floci for real.

## Image tests (`image` tag)

`make image-test` (= `go test -tags image ./tests/image/`) is the only layer that tests the **built artifact** rather than the packages inside it: it builds both the reconciler and the webconsole image, starts each under the Lambda Runtime Interface Emulator that ships in the `provided:al2023` base image, and invokes it over HTTP. The Lambda handler, the JSON request/response contract, the `lambda.norpc` build tag and the Lambda Web Adapter bundled into the image are only exercised here — unit and integration tests both call the package directly. It lives in `tests/` rather than `internal/` for the same reason: it imports nothing from the images it drives, so it belongs to no package inside them. The RIE is how an image is driven, not what is being tested — the harness that owns it lives in `harness_test.go`, and what each image must do lives in its own file. (`tests/image/doc.go` carries no build tag, so the package is never empty when `image` is off.)

What it sends, against a throwaway state table so the expected responses are fixed.

Reconciler (`reconciler_test.go`):

- `{}` — the periodic and manual payload ([setup.md](../usage/setup.md) §8) → a Summary with nothing reconciled
- every `internal/app/reconcile/testdata/rds-event-*.json`, verbatim, as EventBridge delivers it → a Summary, plus one `event-received` line with `source: aws.rds` per fixture in the container's log. The response alone would only show the payload was accepted, not that it was understood as an RDS event
- `[]` — not an object at all → the handler must fail with `unmarshal event` rather than treat it as an empty event and reconcile anyway

Web console (`webconsole_test.go`):

- a `GET /` proxy event from an API Gateway REST API (v1, the production setup) → an HTTP 200 proxy response. That means the adapter started as an extension, rebuilt the event into a loopback HTTP request, and turned the response back into the event's response shape; a missing extension or a port mismatch fails right here
- the same event with a `x-amzn-request-context` header the client forged → the `client` in the console's log is the event's `requestContext.identity.sourceIp`, and the forged IP appears nowhere in the log. With an IP allowlist as the only access control, the IP worth logging is the one that was actually matched against it ([../architecture/web_console.md](../architecture/web_console.md), Japanese). That the adapter overwrites the header can only be confirmed through the image

It sits on its own build tag rather than `integration` because it builds the images first: about 90 seconds cold, a few seconds once Docker's layer cache is warm. The Dockerfile's `COPY . .` means any changed file in the repo — documentation included — invalidates that cache, so expect the slow path more often than a Go-only view of the tree would suggest. Nothing else is needed — Floci and the state table come up with the test, through the same `internal/devtools/emutest` helper the integration tests use, and the container reaches the emulator over `host.docker.internal`.

The build shells out to `docker build`, deliberately: the `Dockerfile` is BuildKit-only (`# syntax` directive, `FROM --platform=$BUILDPLATFORM`, `TARGETOS`/`TARGETARCH`), and testcontainers-go's own image build goes through the legacy `/build` API, which has none of those. Going through the CLI also means the test builds exactly what `make image-reconciler` / `make image-webconsole` builds.

## Fixtures

RDS event samples used by the reconciler tests are in `internal/app/reconcile/testdata/`; they double as reference payloads for the EventBridge rule pattern (verify changes with `aws events test-event-pattern`), and `make image-test` replays every one of them against the built image. A new fixture is picked up automatically — the test globs the directory.

## Mocks

Tests use [testify](https://github.com/stretchr/testify) assertions, with two kinds of test double split by layer.

**Generated (gomock), at the AWS SDK boundary.** Each package declaring an SDK-client interface generates its doubles into a `mocks/` subpackage next to it (`//go:generate` directives live next to the interface): `internal/state`, `internal/aws/compute`, `internal/aws/tagging`, `internal/aws/sns`, and `internal/devtools/devseed`. These interfaces are wide and their argument types are fat, so generation earns its keep; `-typed` makes the `EXPECT()` recorders type-checked, so a wrong `Return`/`DoAndReturn` signature is a compile error rather than a runtime failure. `internal/state/mocks/dynastore.go` is hand-written, not generated — it wires the generated `MockAPI` to an in-memory table so tests can `Seed`/`Item`/`FailOn`/`SetScanPageSize` against it the same way the store's real Scan/GetItem/PutItem/UpdateItem/DeleteItem behave.

**Hand-written, at the application's own ports.** `internal/app/port/porttest` holds plain doubles for `Discoverer`/`Target`/`Describer`/`Notifier`, shared by `internal/app/{reconcile,groups}` and `internal/ui/*`. Those ports are seven small methods over cheapskate's own `model` types, and their tests want stateful behavior (canned observations, recorded stop/start calls) rather than per-call expectations — a generated mock only ever got wrapped in a double like these, so there is nothing to generate here.

```console
make generate     # go generate ./... — regenerate every package's mocks/ after changing an interface
```

Generated files are committed (there is no CI to regenerate them), so run `make generate` and include the diff whenever you add or change a mocked interface method.
