# Building

This document covers building the binaries and the container images, and pushing to ECR. Every entry point is a `make` target.

## Binaries

Three targets wrap `go build`.

```console
make build       # runs go build ./...
make cli         # produces bin/cheapskate-cli
make webconsole  # produces bin/webconsole
```

## Container images

The reconciler and the web console are separate images, each carrying one binary. Both are built from the same `Dockerfile`, selected with `--target`.

The build targets for the images and the binary each carries are collected below.

| Target | Image | Binary |
| --- | --- | --- |
| `reconciler` | `cheapskate-reconciler` | `./cmd/reconciler` |
| `webconsole` | `cheapskate-webconsole` | `./cmd/webconsole` |

Examples of running the build are given below.

```console
make image                                   # both, linux/arm64, tag dev
make image-reconciler                        # cheapskate-reconciler:dev only
make image-webconsole                        # cheapskate-webconsole:dev only
make image PLATFORM=linux/amd64 TAG=v0.1.0   # the x86 build
```

The base image is `public.ecr.aws/lambda/provided:al2023`. Both binaries are placed as `/var/runtime/bootstrap`, so no `ImageConfig.EntryPoint` override is needed. Without the web console, only the reconciler has to be built and pushed.

The Dockerfile cross-compiles from the host platform through `GOARCH`, so building for another architecture needs no emulation. The Go build stage is shared between the two images, so building both downloads the dependencies once.

### Lambda Web Adapter

The web console image carries the Lambda Web Adapter executable as `/opt/extensions/lambda-adapter`, pinned to a version in the `Dockerfile`. It is the only runtime dependency outside go.mod, and it is updated through Dependabot's docker updates rather than with the Go modules.

For how the adapter converts invocation events into HTTP, and where the application depends on it, see [../architecture/on_lambda.md](../architecture/on_lambda.md). For how a Dependabot update reaches a release, see the dependency updates section in [release.md](release.md).

## Testing the images

Before deploying, confirm that the built images start as Lambda runtimes.

```console
make image-test   # builds both images, starts each on the Lambda RIE, and invokes it
```

This catches a corrupt image or a missing handler. All it needs is docker; the emulator and a disposable state table come up alongside the tests. For the payloads sent and the responses expected, see the image tests section in [test.md](test.md).

## Pushing to ECR

Create one repository per image. Creating them through to pushing is shown below.

```console
aws ecr create-repository --repository-name cheapskate-reconciler   # first time only
aws ecr create-repository --repository-name cheapskate-webconsole   # first time only (when deploying the web console)
make push \
  ECR_REPO_RECONCILER=<account>.dkr.ecr.<region>.amazonaws.com/cheapskate-reconciler \
  ECR_REPO_WEBCONSOLE=<account>.dkr.ecr.<region>.amazonaws.com/cheapskate-webconsole \
  TAG=v0.1.0
```

`make push` runs `docker build`, `docker login`, and `docker push` for both images. For one of them, use `make push-reconciler` or `make push-webconsole` and give only the matching `ECR_REPO_*`.

> [!IMPORTANT]
> Match the image's platform to the Lambda function's architecture (`arm64` ↔ `linux/arm64`). A mismatched image is rejected when the function is created or updated.
