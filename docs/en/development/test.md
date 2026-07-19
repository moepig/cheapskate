# Testing

```console
make unit         # AWS-free unit tests (go test ./...)
make integration  # tests tagged `integration`; needs the local emulator (below)
make test         # both
make lint         # gofmt + go vet (with the integration tag)
```

## Integration tests and the local emulator

Integration tests run against **Floci**, a local AWS emulator, started with:

```console
make floci-up     # docker compose up + wait for http://localhost:4566/_localstack/health
make floci-down
```

- Wiring lives in `internal/emutest`; connectivity uses only the standard `AWS_ENDPOINT_URL` environment variable (default `http://localhost:4566`), so production code stays emulator-unaware. Set `AWS_ENDPOINT_URL` to point the tests at an emulator elsewhere.
- If no emulator is reachable, integration tests **skip** rather than fail.
- Floci mounts the docker socket (see `compose.yaml`) because its RDS/ECS emulation runs real containers.

## Fixtures

RDS event samples used by the reconciler tests are in `internal/reconcile/testdata/`; they double as reference payloads for the EventBridge rule pattern (verify changes with `aws events test-event-pattern`).

## Mocks

Tests use [testify](https://github.com/stretchr/testify) assertions and [go.uber.org/mock](https://github.com/uber-go/mock) (gomock) doubles, generated into `internal/mocks/` from the interfaces in `internal/store`, `internal/target`, and `internal/reconcile` (`//go:generate` directives live next to each interface). `internal/mocks/dynastore.go` is hand-written, not generated — it wires a generated `MockStoreAPI` to an in-memory table so tests can `Seed`/`Item`/`FailOn`/`SetScanPageSize` against it the same way the store's real Scan/GetItem/PutItem/UpdateItem/DeleteItem behave.

```console
make generate     # go generate ./... — regenerate internal/mocks/{store,target,reconcile}.go after changing an interface
```

Generated files are committed (there is no CI to regenerate them), so run `make generate` and include the diff whenever you add or change a mocked interface method.
