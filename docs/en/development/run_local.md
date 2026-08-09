# Running locally

This document covers running cheapskate in a development environment. There are two routes: starting everything at once with `make dev`, and starting the components individually. Both connect to Floci, the local AWS emulator.

## make dev

This goes from the emulator through to seeding sample data in one step.

```console
make dev       # emulator + state table + sample group + web console
make dev-down  # stops the emulator
```

`scripts/dev.sh` runs the following in order.

1. Starts the emulator with `docker compose up -d` and waits for the health check
2. Creates the state table `cheapskate-dev` with `go run ./cmd/dev-bootstrap`
3. Seeds the sample group `dev` through `cheapskate-cli`
4. Creates dummy ECS resources
5. Starts the web console in the foreground at `http://127.0.0.1:8080/`

Every step goes through `go run`, so a code change takes effect on the next request or the next run. Ctrl-C stops the web console, and `make dev-down` stops the emulator.

The only things matching the sample group's selector are the dummy ECS services. Types other than ECS yield either an empty resource list or a discovery error.

Since the web console holds the foreground, using `cheapskate-cli` from another shell means giving it the endpoint and credentials. An example is given below.

```console
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export CHEAPSKATE_TABLE=cheapskate-dev

go run ./cmd/cheapskate-cli list
```

> [!WARNING]
> Run without these and it tries to reach real AWS rather than the emulator. With valid credentials, it operates on the resources in that account.

## Starting the components individually

Each component can connect to the local emulator or to real AWS. Using the emulator, the settings are as follows.

```console
make floci-up
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
```

There are no dummy resources for types other than ECS because of what the emulator reproduces. For why switching the endpoint takes nothing but `AWS_ENDPOINT_URL`, the extent of the AWS APIs that cannot be reproduced, and the substitutes for them, see [../architecture/emulation_local.md](../architecture/emulation_local.md).

Create the state table beforehand: set `CHEAPSKATE_TABLE` and run `go run ./cmd/dev-bootstrap`.

### Web console

Give it the state table name through the environment and start it.

```console
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/webconsole          # http://127.0.0.1:8080/
go run ./cmd/webconsole -addr 127.0.0.1:9090                       # a different port
```

### cheapskate-cli

Likewise give it the state table name and run a subcommand.

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli list
go run ./cmd/cheapskate-cli set-selector --group dev --tag-key cheapskate:group --tag-value dev --types rds-instance
go run ./cmd/cheapskate-cli pin --group dev stopped
```

### Reconciler

The reconciler is a Lambda entrypoint, so it runs inside a container through the Runtime Interface Emulator. Building the image and starting it is shown below.

```console
make image-reconciler
docker run --rm -p 9000:8080 \
  --add-host host.docker.internal:host-gateway \
  -e STATE_TABLE_NAME=cheapskate-state \
  -e AWS_ENDPOINT_URL=http://host.docker.internal:4566 \
  -e AWS_REGION=ap-northeast-1 -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  cheapskate-reconciler:dev
```

Invocations go into the running container over HTTP from another shell.

```console
# `{}` triggers a full reconcile
curl -d '{}' http://localhost:9000/2015-03-31/functions/function/invocations
```

`host.docker.internal` is how the container reaches the emulator on the host. To run against real AWS, drop `AWS_ENDPOINT_URL` and supply real credentials.

`make image-test` automates the same startup and invocation and feeds it the real events from `testdata`. To exercise the reconcile loop alone, the integration tests (`make integration`) suffice and need no image build. For what each covers, see the test layers in [test.md](test.md).
