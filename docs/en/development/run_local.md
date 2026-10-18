# Running locally

This document covers the development environment using a local AWS emulator and startup of individual components.

Integration and image tests use the same emulator. Adapter unit tests verify request selection and validation for control-plane behavior the emulator does not reproduce.

## Complete development environment

Docker is required. `make dev` starts the AWS emulator, creates the DynamoDB table and tagged ECS fixtures, writes a sample schedule, and runs the web console.

Run `make dev` to start the environment. After using it, run `make dev-down` to stop the emulator:

```console
make dev
make dev-down
```

The web console listens on `http://127.0.0.1:8080/`. `make dev` defaults to Asia/Tokyo and seeds a weekday 09:00 start / 21:00 stop schedule; existing environment variables override the defaults.

From another shell, use the same emulator settings. Without the AWS_ENDPOINT_URL environment variable, an AWS SDK client may use real AWS credentials and resources:

```console
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export CHEAPSKATE_TABLE=cheapskate-dev
export DEFAULT_TIMEZONE=Asia/Tokyo

go run ./cmd/cheapskate-cli list
go run ./cmd/cheapskate-cli show --group dev
```

## Individual components

Set the AWS connection environment variables and time zone above in the same shell.

### Prepare the emulator and table

Start the emulator and create a table before running components separately:

```console
make floci-up
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/dev-bootstrap
```

### Start the web console

Create group configuration and run the web console:

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli schedule --group dev -start '0 8 * * 1-5' -stop '0 20 * * 1-5'
STATE_TABLE_NAME=cheapskate-state go run ./cmd/webconsole
```

### Invoke the reconciler

The reconciler runs through the Lambda Runtime Interface Emulator in its container image. Build the image and start it with `docker run`, then execute the final `curl` command in another shell:

```console
make image-reconciler
docker run --rm -p 9000:8080 \
  --add-host host.docker.internal:host-gateway \
  -e STATE_TABLE_NAME=cheapskate-state \
  -e AWS_ENDPOINT_URL=http://host.docker.internal:4566 \
  -e DEFAULT_TIMEZONE=Asia/Tokyo \
  -e AWS_REGION=ap-northeast-1 \
  -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  cheapskate-reconciler:dev

curl -d '{}' http://localhost:9000/2015-03-31/functions/function/invocations
```

Every invocation payload triggers the same full reconcile.
