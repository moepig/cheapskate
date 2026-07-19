# Container image settings. ECR_REPO is the full repository URI in YOUR account, e.g. 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate
IMAGE    ?= cheapskate
TAG      ?= dev
PLATFORM ?= linux/arm64
ECR_REPO ?=

.PHONY: build generate unit integration test lint fmt vet image push cli webconsole floci-up floci-down smoke dev dev-down

build:
	go build ./...

generate:
	go generate ./...

cli:
	go build -o bin/cheapskate-cli ./cmd/cheapskate-cli

webconsole:
	go build -o bin/webconsole ./cmd/webconsole

unit:
	go test ./...

# Requires a local AWS emulator (make floci-up).
integration:
	go test -tags integration -count=1 ./...

test: unit integration

fmt:
	gofmt -l . | tee /dev/stderr | wc -l | grep -q '^0$$'

vet:
	go vet -tags integration ./...

lint: fmt vet

image:
	docker build --platform $(PLATFORM) -t $(IMAGE):$(TAG) .

# Self-push the image to an ECR repository in your own account.
push: image
	@test -n "$(ECR_REPO)" || (echo "set ECR_REPO=<account>.dkr.ecr.<region>.amazonaws.com/<repo>" && exit 1)
	aws ecr get-login-password | docker login --username AWS --password-stdin $(firstword $(subst /, ,$(ECR_REPO)))
	docker tag $(IMAGE):$(TAG) $(ECR_REPO):$(TAG)
	docker push $(ECR_REPO):$(TAG)

floci-up:
	docker compose up -d
	@until curl -sf http://localhost:4566/_localstack/health > /dev/null; do sleep 1; done
	@echo "floci ready at http://localhost:4566"

floci-down:
	docker compose down

# A-1: builds the image and drives it through the Lambda Runtime Interface Emulator, checking
# both the reconciler (default) and webconsole (opt-in) entrypoints actually boot and respond.
# Requires a running emulator (make floci-up) and the AWS CLI.
smoke:
	./scripts/smoke.sh

# One-command local bring-up: floci + state table + sample "dev" tag + web console on
# http://127.0.0.1:8080/, all via `go run`. Ctrl-C stops the console; `make dev-down` stops floci.
dev:
	./scripts/dev.sh

dev-down:
	docker compose down
