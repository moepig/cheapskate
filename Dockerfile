# syntax=docker/dockerfile:1
# Reconciler Lambda container image. Build and push to an ECR repository in your own account (see README "Deploy"), e.g.:
#   docker build --platform linux/arm64 -t <account>.dkr.ecr.<region>.amazonaws.com/cheapskate:v0.1.0 .

# Build on the host platform and cross-compile via GOARCH, so no emulation is needed when building arm64 images on x86 hosts (and vice versa).
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags lambda.norpc -trimpath -ldflags="-s -w" \
    -o /bootstrap ./cmd/reconciler && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -tags lambda.norpc -trimpath -ldflags="-s -w" \
    -o /webconsole ./cmd/webconsole

FROM public.ecr.aws/lambda/provided:al2023
COPY --from=build /bootstrap /var/runtime/bootstrap
# Opt-in web console entrypoint; selected via an ImageConfig EntryPoint override on a second function (see docs/en/usage/setup.md). Inert otherwise.
COPY --from=build /webconsole /var/runtime/webconsole
# provided.al2023 runs /var/runtime/bootstrap; the CMD is unused but required to be non-empty by some tooling.
CMD ["handler"]
