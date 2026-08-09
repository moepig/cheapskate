# Building

Prerequisites: Go 1.26+; Docker (BuildKit) for the container image.

## Binaries

```console
make build       # go build ./... — compile everything
make cli         # bin/cheapskate-cli — the operator CLI
make webconsole  # bin/webconsole — the web console (local mode)
```

## Container images

The reconciler and the web console are **separate images**, one binary each, both from the same `Dockerfile` via a `--target`. Each ships its binary as `/var/runtime/bootstrap` on `public.ecr.aws/lambda/provided:al2023`, so neither function needs an `ImageConfig` entrypoint override. The web console is optional: if you don't deploy it, build and push only the reconciler.

| Target | Image | Binary |
|---|---|---|
| `reconciler` | `cheapskate-reconciler` | `./cmd/reconciler` |
| `webconsole` | `cheapskate-webconsole` | `./cmd/webconsole` |

The web console image carries one more executable: the [Lambda Web Adapter](https://github.com/awslabs/aws-lambda-web-adapter), as `/opt/extensions/lambda-adapter`. The console itself is a plain HTTP server that knows nothing about Lambda; the extension does the translating between invocations and HTTP. Its version is pinned in the `Dockerfile` — the one runtime dependency outside go.mod, and it moves by Dependabot's docker updates rather than with the Go modules ([release.md](release.md)).

```console
make image                                   # both, linux/arm64, tag "dev"
make image-reconciler                        # cheapskate-reconciler:dev only
make image-webconsole                        # cheapskate-webconsole:dev only
make image PLATFORM=linux/amd64 TAG=v0.1.0   # x86 variant
```

The Dockerfile cross-compiles via `GOARCH` from the host platform, so building arm64 images on x86 hosts (and vice versa) needs no emulation. The Go build stage is shared, so building both costs one dependency download.

## Testing the images

```console
make image-test   # build both images, boot each under the Lambda RIE, and invoke it
```

Both images are run under `aws-lambda-rie` (bundled in the `public.ecr.aws/lambda/provided:al2023` base image) and invoked over HTTP, so a broken image or a build tag that silently drops a handler is caught before deploy. The reconciler gets the real EventBridge payloads; the web console gets an API Gateway proxy event, which takes it through the Lambda Web Adapter, so a missing extension or a port mismatch fails the same way — that case also asserts the logged client IP is the event's `sourceIp` and not a forged header. Docker is the only prerequisite: the emulator and a throwaway state table come up with the test.

See [test.md](test.md) for the breakdown.

## Pushing to your ECR

One repository per image:

```console
aws ecr create-repository --repository-name cheapskate-reconciler   # once
aws ecr create-repository --repository-name cheapskate-webconsole   # once, only if you deploy the console
make push \
  ECR_REPO_RECONCILER=<account>.dkr.ecr.<region>.amazonaws.com/cheapskate-reconciler \
  ECR_REPO_WEBCONSOLE=<account>.dkr.ecr.<region>.amazonaws.com/cheapskate-webconsole \
  TAG=v0.1.0
```

`make push` wraps `docker build`, `docker login` (via `aws ecr get-login-password`), and `docker push` for both images; `make push-reconciler` and `make push-webconsole` do one each, and only the corresponding `ECR_REPO_*` needs to be set. The image platform must match the Lambda function's architecture (`arm64` ↔ `linux/arm64`).
