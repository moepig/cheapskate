# syntax=docker/dockerfile:1
# Two separate Lambda container images out of one Dockerfile, selected with --target:
# `reconciler` (required) and `webconsole` (optional). Each ships a single binary as
# /var/runtime/bootstrap, so neither needs an ImageConfig entrypoint override. Build and
# push to ECR repositories in your own account (see README "Deploy"), e.g.:
#   docker build --platform linux/arm64 --target reconciler \
#     -t <account>.dkr.ecr.<region>.amazonaws.com/cheapskate-reconciler:v0.1.0 .
# The reconciler is the last stage, so a bare `docker build .` builds that one.

# Build on the host platform and cross-compile via GOARCH, so no emulation is needed when building arm64 images on x86 hosts (and vice versa).
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

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

# The Lambda Web Adapter, an external extension that speaks the Lambda Runtime API on the web
# console's behalf and forwards each invocation to it as an ordinary HTTP request. Multi-arch, so
# the tag alone resolves to the right build for --platform. Pinned to an exact version: unlike the
# Go dependencies it is not in go.mod, so nothing else would notice it moving.
FROM public.ecr.aws/awsguru/aws-lambda-adapter:1.1.0 AS lambda-adapter

FROM public.ecr.aws/lambda/provided:al2023 AS webconsole
# Lambda's init starts every executable under /opt/extensions before invoking the function.
COPY --from=lambda-adapter /lambda-adapter /opt/extensions/lambda-adapter
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

FROM public.ecr.aws/lambda/provided:al2023 AS reconciler
COPY --from=build-reconciler /bootstrap /var/runtime/bootstrap
CMD ["handler"]
