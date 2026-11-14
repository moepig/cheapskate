# Building

This document covers building the binaries and the container images, and pushing to ECR. Every entry point is a `make` target.

## Binaries

Three targets run `go build`.

```console
make build       # runs go build ./...
make cli         # produces bin/cheapskate-cli
make webconsole  # produces bin/webconsole
```

## Container images

The reconciler and the web console are separate images, each carrying one binary. Both are built from the same `Dockerfile`, selected with `--target`. Without the web console, only the reconciler has to be built and pushed.

The build targets for the images and the binary each carries are collected below.

| Dockerfile target | Image | Binary |
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

Both binaries are placed as `/var/runtime/bootstrap`, so no `ImageConfig.EntryPoint` override is needed.

Container images are pinned by both tag and digest. This includes the Go and Rust builders, Lambda base images, and Dockerfile frontend. Tags identify the intended version; digests fix the referenced content. The Lambda base images come from AWS's `amazon/aws-lambda-provided` repository on Docker Hub. Separate `al2023-x86_64` and `al2023-arm64` stages are selected through `TARGETARCH`.

Image builds do not pull from ECR Public, so they do not need AWS credentials or an IAM role for dependency downloads. Docker Hub, GitHub, Rust toolchain downloads, Alpine packages, and crates.io must be reachable.

The Dockerfile cross-compiles from the host platform through `GOARCH`, so building for another architecture needs no emulation.

The Go build stage is shared between the two images, so building both downloads the dependencies once.

An allowlist in `.dockerignore` limits the build context to the Dockerfile, module files, `cmd/`, and `internal/`. The Dockerfile also copies only the required directories instead of using `COPY . .`. This keeps development files and credentials out of the context and image, and avoids cache invalidation from unrelated changes.

### Lambda Web Adapter

The web console image carries Lambda Web Adapter v1.1.0 as `/opt/extensions/lambda-adapter`. The `lambda-adapter` stage builds the official GitHub source from a pinned commit archive, verified with SHA-256 by `ADD --checksum`. Cargo uses the upstream lockfile with `--locked`. The adapter's Apache license and notice are included under `/opt/licenses/aws-lambda-web-adapter/`.

Rust cross-compiles a static musl binary for the selected architecture using its bundled LLD linker. No target-architecture emulation is needed. The first build compiles the adapter; later builds reuse Docker layers and Cargo caches.

For how the adapter converts invocation events into HTTP, and its relationship to the web-console application, see [Lambda runtime](../architecture/overview.md#lambda-runtime). For updating the pinned source, see the dependency updates section in [Releases and dependency updates](release.md).

## Testing the images

Before deploying, confirm that the built images start as Lambda runtimes.

```console
make image-test   # builds both images, starts each on the Lambda RIE, and invokes it
```

This catches a corrupt image or a missing handler. Docker is required; the emulator and a disposable state table come up alongside the tests. For the payloads sent and the responses expected, see the image tests section in [Testing](test.md).

## Pushing to ECR

> [!IMPORTANT]
> Match the image's platform to the Lambda function's architecture (`arm64` ↔ `linux/arm64`). A mismatched image is rejected when the function is created or updated.

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
