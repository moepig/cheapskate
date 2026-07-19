# Building

Prerequisites: Go 1.26+; Docker (BuildKit) for the container image.

## Binaries

```console
make build       # go build ./... — compile everything
make cli         # bin/cheapskate-cli — the operator CLI
make webconsole  # bin/webconsole — the web console (local mode)
```

## Container image

The image bundles two static binaries on `public.ecr.aws/lambda/provided:al2023`: `/var/runtime/bootstrap` (the reconciler, default entrypoint) and `/var/runtime/webconsole` (selected via an `ImageConfig` entrypoint override on the optional web console function).

```console
make image                                   # cheapskate:dev for linux/arm64
make image PLATFORM=linux/amd64 TAG=v0.1.0   # x86 variant
```

The Dockerfile cross-compiles via `GOARCH` from the host platform, so building arm64 images on x86 hosts (and vice versa) needs no emulation.

## Smoke-testing the image

```console
make floci-up   # start the local AWS emulator, once
make smoke      # build the image, boot both entrypoints under the Lambda RIE, and invoke them
```

`make smoke` (`scripts/smoke.sh`) builds the image, then runs it under `aws-lambda-rie` (bundled in the `public.ecr.aws/lambda/provided:al2023` base image) twice: once with the default entrypoint (`/var/runtime/bootstrap`, the reconciler) and once with the entrypoint overridden to `/var/runtime/webconsole`, mirroring the `ImageConfig.EntryPoint` override used in production. Each is `curl`-invoked and the response is checked — a reconciler Summary JSON for the first, an HTTP 200 for the second — so a broken entrypoint or a build tag that silently drops a handler is caught before deploy. Requires the AWS CLI and a running emulator (`make floci-up`).

## Pushing to your ECR

```console
aws ecr create-repository --repository-name cheapskate   # once
make push ECR_REPO=<account>.dkr.ecr.<region>.amazonaws.com/cheapskate TAG=v0.1.0
```

`make push` wraps `docker build`, `docker login` (via `aws ecr get-login-password`), and `docker push`. The image platform must match the Lambda function's architecture (`arm64` ↔ `linux/arm64`).
