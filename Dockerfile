# syntax=docker/dockerfile:1@sha256:ecfaec9ed6d810b56388c508f4121597bfbba70d41a6dfeee4d8cad5f295fc32
# Two separate Lambda container images out of one Dockerfile, selected with --target:
# `reconciler` (required) and `webconsole` (optional). Each ships a single binary as
# /var/runtime/bootstrap, so neither needs an ImageConfig entrypoint override. Build and
# push to ECR repositories in your own account (see README "Deploy"), e.g.:
#   docker build --platform linux/arm64 --target reconciler \
#     -t <account>.dkr.ecr.<region>.amazonaws.com/cheapskate-reconciler:v0.1.0 .
# The reconciler is the last stage, so a bare `docker build .` builds that one.

# Build on the host platform and cross-compile via GOARCH, so no emulation is needed when building arm64 images on x86 hosts (and vice versa).
FROM --platform=$BUILDPLATFORM golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# The web console is a plain HTTP server: it links no Lambda runtime library at all, hence no
# lambda.norpc tag here (the reconciler, which does use aws-lambda-go, still needs it).
FROM build AS build-webconsole
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" \
    -o /bootstrap ./cmd/webconsole

FROM build AS build-reconciler
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags lambda.norpc -trimpath -ldflags="-s -w" \
    -o /bootstrap ./cmd/reconciler

# Runtime stages are selected by TARGETARCH.
FROM amazon/aws-lambda-provided:al2023-x86_64@sha256:4999d3a0e62f5eefe8eb0236ff2085a9f016e49badbfd4556b9ddaad0b13ff26 AS lambda-runtime-amd64
FROM amazon/aws-lambda-provided:al2023-arm64@sha256:9e1b7d09646700454ad9a6c8207970a14f4bc5ee2868d80739ad00f8eb50d81c AS lambda-runtime-arm64

# Lambda Web Adapter v1.1.0, built from a verified source archive for the target architecture.
FROM --platform=$BUILDPLATFORM rust:1.99.0-alpine3.24@sha256:0cce0a5e0e8ba67b455257a3a02a1d99005f382748789d6464460028810f1627 AS lambda-adapter
WORKDIR /src
RUN apk add --no-cache gcc musl-dev
ADD --checksum=sha256:41e437baff00539d5cd4bf6c7a9f9f6106386b9cf5461c38c19b969b94b568ea https://github.com/aws/aws-lambda-web-adapter/archive/596bdbcea78a89084fd0b522807d04cd2f1f3f7e.tar.gz /tmp/lambda-adapter.tar.gz
# Build scripts use the native C linker; RUSTFLAGS selects LLD for target binaries.
RUN tar -xzf /tmp/lambda-adapter.tar.gz --strip-components=1 -C /src \
    && rm .cargo/config.toml \
    && rustup target add x86_64-unknown-linux-musl aarch64-unknown-linux-musl
ARG TARGETARCH
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,id=cheapskate-lambda-adapter-${TARGETARCH},target=/src/target \
    case "$TARGETARCH" in \
      amd64) target=x86_64-unknown-linux-musl ;; \
      arm64) target=aarch64-unknown-linux-musl ;; \
      *) echo "Unsupported Lambda architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
    && RUSTFLAGS="-C linker=$(rustc --print target-libdir)/../bin/rust-lld" \
       LAMBDA_RUNTIME_USER_AGENT=aws-lambda-rust/aws-lambda-adapter/1.1.0 \
       cargo build --release --locked --bin lambda-adapter --target "$target" \
    && cp "target/$target/release/lambda-adapter" /lambda-adapter

FROM lambda-runtime-${TARGETARCH} AS webconsole
# Lambda's init starts every executable under /opt/extensions before invoking the function.
COPY --from=lambda-adapter /lambda-adapter /opt/extensions/lambda-adapter
COPY --from=lambda-adapter /src/LICENSE /src/NOTICE /opt/licenses/aws-lambda-web-adapter/
COPY --from=build-webconsole /bootstrap /var/runtime/bootstrap
# The adapter connects to AWS_LWA_PORT; the server reads PORT. Both are set here so the image
# carries its own contract. Not 8080: the Runtime Interface Emulator binds that port when this
# image is run as a Lambda locally (tests/image), and the two would collide.
ENV PORT=8000 AWS_LWA_PORT=8000
# Readiness is "the port is open", not "GET / answers". The default HTTP check would make every
# cold start render the group list, and that reads DynamoDB.
ENV AWS_LWA_READINESS_CHECK_PROTOCOL=tcp
# provided.al2023 runs /var/runtime/bootstrap; the CMD is unused but required to be non-empty by some tooling.
CMD ["handler"]

FROM lambda-runtime-${TARGETARCH} AS reconciler
COPY --from=build-reconciler /bootstrap /var/runtime/bootstrap
CMD ["handler"]
