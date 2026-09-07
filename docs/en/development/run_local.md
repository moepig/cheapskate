# Running locally

## Complete development environment

Docker is required. The convenience target starts the emulator, creates the table, creates tagged ECS fixtures, writes a sample schedule, and runs the web console:

```console
make dev
make dev-down
```

The web console listens on `http://127.0.0.1:8080/`. Make dev defaults to Asia/Tokyo and seeds a weekday 09:00 start / 21:00 stop schedule; existing environment variables override the defaults. From another shell, use the same emulator settings:

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

Without the endpoint variable, an AWS SDK client may use real AWS credentials and resources.

## Individual components

Set the AWS connection environment variables and time zone above in the same shell, then start the emulator and create a table before running components separately:

```console
make floci-up
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/dev-bootstrap
```

Create configuration and run the web console:

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli schedule --group dev -start '0 8 * * 1-5' -stop '0 20 * * 1-5'
STATE_TABLE_NAME=cheapskate-state go run ./cmd/webconsole
```

The reconciler entry point runs through the Lambda Runtime Interface Emulator in its container image:

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
