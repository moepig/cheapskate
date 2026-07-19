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
