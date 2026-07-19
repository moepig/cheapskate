# Running locally

All components can run on your machine, either against the local emulator (Floci, `make floci-up`, endpoint `http://localhost:4566`) or against a real AWS account using your own credentials. To target the emulator, export:

```console
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
```

Create a state table first (same schema as production — see `docs/en/usage/setup.md` §2); against Floci the same `aws dynamodb create-table` command works.

## Web console

Plain HTTP server in local mode; no Lambda involved:

```console
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/webconsole          # http://127.0.0.1:8080/
go run ./cmd/webconsole -addr 127.0.0.1:9090                       # different port
```

## cheapskate-cli

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli list
go run ./cmd/cheapskate-cli pin rds-instance#dev-db stopped
```

## Reconciler

The reconciler is a Lambda entrypoint, so it runs inside the container image via the Runtime Interface Emulator that ships with the `provided:al2023` base image:

```console
make image
docker run --rm -p 9000:8080 \
  --add-host host.docker.internal:host-gateway \
  -e STATE_TABLE_NAME=cheapskate-state \
  -e AWS_ENDPOINT_URL=http://host.docker.internal:4566 \
  -e AWS_REGION=ap-northeast-1 -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  cheapskate:dev

# in another shell — `{}` triggers a full reconcile:
curl -d '{}' http://localhost:9000/2015-03-31/functions/function/invocations
```

(`host.docker.internal` lets the container reach Floci on the host; against real AWS, drop `AWS_ENDPOINT_URL` and pass real credentials instead.)

Alternatively, the integration tests (`make integration`) exercise the full reconcile loop against Floci without running the image — usually the faster feedback loop.
