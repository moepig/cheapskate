# Running locally

## Quick start: `make dev`

```console
make dev       # floci + state table + a sample "dev" group (selector + schedule) + web console
```

This starts Floci (`docker compose`), waits for it to be healthy, creates a `cheapskate-dev` state table and dummy ECS resources (via `cmd/dev-bootstrap`, idempotently — safe to re-run; see below), seeds a sample group through the real `cheapskate-cli` (so the seed commands double as usage examples), and runs the web console in the foreground on `http://127.0.0.1:8080/`. Everything runs via `go run`, so code changes are picked up on the next request or the next `make dev`.

Ctrl-C stops the console; `make dev-down` stops Floci. **Dummy ECS data**: `internal/devtools/devseed` creates an ECS cluster (`dev-cluster`) with three services — `api` and `worker` tagged `cheapskate:group=dev` (matching the seeded "dev" group's selector), and `worker` also carrying the ECS scaling tags (`cheapskate/desired-count`, `cheapskate/scaling-min`, `cheapskate/scaling-max`) so they're visible in the group page/`cheapskate-cli show` resource view; `batch` is left untagged to show what a non-matching resource looks like. Tags are applied via an explicit `resourcegroupstaggingapi.TagResources` call, not ECS's own `--tags`, because Floci does not reflect create-time service tags into `tag:GetResources` on its own.

**Caveat**: there are no dummy RDS or EC2 resources, and Floci's Resource Groups Tagging API (`tag:GetResources`) support for those is otherwise limited, so the "dev" group's RDS/EC2 rows will likely still show nothing beyond the two ECS services, or an inline discover error. That's expected: the console/CLI are built to degrade gracefully instead of failing (see [../usage/operations.md](../usage/operations.md)). The integration tests (`make integration`) inject a stub `Discoverer` instead of depending on Floci's Tagging API support, and are the better way to exercise the full reconcile loop end to end.

## Manual, per-component setup

All components can also run individually, either against the local emulator (Floci, `make floci-up`, endpoint `http://localhost:4566`) or against a real AWS account using your own credentials. To target the emulator, export:

```console
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
```

Create a state table first (same schema as production — see `docs/en/usage/setup.md` §2); `go run ./cmd/dev-bootstrap` (with `CHEAPSKATE_TABLE` set) creates it, or the same `aws dynamodb create-table` command from setup.md works directly against Floci.

### Web console

Plain HTTP server in local mode; no Lambda involved:

```console
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/webconsole          # http://127.0.0.1:8080/
go run ./cmd/webconsole -addr 127.0.0.1:9090                       # different port
```

### cheapskate-cli

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli list
go run ./cmd/cheapskate-cli set-selector --group dev --tag-key cheapskate:group --tag-value dev --types rds-instance
go run ./cmd/cheapskate-cli pin --group dev stopped
```

### Reconciler

The reconciler is a Lambda entrypoint, so it runs inside the container image via the Runtime Interface Emulator that ships with the `provided:al2023` base image:

```console
make image-reconciler
docker run --rm -p 9000:8080 \
  --add-host host.docker.internal:host-gateway \
  -e STATE_TABLE_NAME=cheapskate-state \
  -e AWS_ENDPOINT_URL=http://host.docker.internal:4566 \
  -e AWS_REGION=ap-northeast-1 -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  cheapskate-reconciler:dev

# in another shell — `{}` triggers a full reconcile:
curl -d '{}' http://localhost:9000/2015-03-31/functions/function/invocations
```

(`host.docker.internal` lets the container reach Floci on the host; against real AWS, drop `AWS_ENDPOINT_URL` and pass real credentials instead.)

`make image-test` (the `image` tag) automates exactly this and feeds the image the real EventBridge payloads from `testdata` — see [test.md](test.md).

Alternatively, the integration tests (`make integration`) exercise the full reconcile loop against Floci without running the image — usually the faster feedback loop.
