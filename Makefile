# Container image settings. The reconciler and the web console are separate images, hence
# separate repositories. ECR_REPO_* is the full repository URI in YOUR account, e.g.
# 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com/cheapskate-reconciler
IMAGE_RECONCILER    ?= cheapskate-reconciler
IMAGE_WEBCONSOLE    ?= cheapskate-webconsole
TAG                 ?= dev
PLATFORM            ?= linux/arm64
ECR_REPO_RECONCILER ?=
ECR_REPO_WEBCONSOLE ?=

.PHONY: build generate unit integration test lint fmt vet cli webconsole \
	image image-reconciler image-webconsole push push-reconciler push-webconsole \
	floci-up floci-down image-test dev dev-down

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

# Spins up the local AWS emulator (Floci) via testcontainers-go automatically; just needs Docker.
# One `cheapskate-itest-floci` container is shared by every test binary and left running for the
# next run to reuse (see internal/devtools/emutest); `make floci-down` removes it.
integration:
	go test -tags integration -count=1 ./...

test: unit integration

fmt:
	gofmt -l . | tee /dev/stderr | wc -l | grep -q '^0$$'

vet:
	go vet -tags integration ./...
	go vet -tags image ./...

lint: fmt vet

image: image-reconciler image-webconsole

image-reconciler:
	docker build --platform $(PLATFORM) --target reconciler -t $(IMAGE_RECONCILER):$(TAG) .

image-webconsole:
	docker build --platform $(PLATFORM) --target webconsole -t $(IMAGE_WEBCONSOLE):$(TAG) .

# Self-push the images to ECR repositories in your own account. The web console is optional,
# so `make push-reconciler` alone is a complete deploy.
push: push-reconciler push-webconsole

push-reconciler: image-reconciler
	@test -n "$(ECR_REPO_RECONCILER)" || (echo "set ECR_REPO_RECONCILER=<account>.dkr.ecr.<region>.amazonaws.com/<repo>" && exit 1)
	aws ecr get-login-password | docker login --username AWS --password-stdin $(firstword $(subst /, ,$(ECR_REPO_RECONCILER)))
	docker tag $(IMAGE_RECONCILER):$(TAG) $(ECR_REPO_RECONCILER):$(TAG)
	docker push $(ECR_REPO_RECONCILER):$(TAG)

push-webconsole: image-webconsole
	@test -n "$(ECR_REPO_WEBCONSOLE)" || (echo "set ECR_REPO_WEBCONSOLE=<account>.dkr.ecr.<region>.amazonaws.com/<repo>" && exit 1)
	aws ecr get-login-password | docker login --username AWS --password-stdin $(firstword $(subst /, ,$(ECR_REPO_WEBCONSOLE)))
	docker tag $(IMAGE_WEBCONSOLE):$(TAG) $(ECR_REPO_WEBCONSOLE):$(TAG)
	docker push $(ECR_REPO_WEBCONSOLE):$(TAG)

floci-up:
	docker compose up -d
	@until curl -sf http://localhost:4566/_localstack/health > /dev/null; do sleep 1; done
	@echo "floci ready at http://localhost:4566"

# Removes both emulators — the compose one and the reused integration-test one — plus the RDS/ECS
# containers Floci spawns through the docker socket, which outlive their emulator.
floci-down:
	docker compose down
	-docker rm -f cheapskate-itest-floci
	-docker ps -aq --filter name=floci- | xargs -r docker rm -f

# Black-box tests of the container images (hence tests/, not internal/): both images are built and
# run as Lambdas through the Runtime Interface Emulator. The reconciler is fed the real EventBridge
# payloads from internal/app/reconcile/testdata; the web console gets an API Gateway proxy event,
# which exercises the Lambda Web Adapter extension that only exists inside the image. On its own
# build tag rather than `integration` because it builds the images first (~90s from cold). Needs
# only Docker; the emulator and the state table come up with the test.
image-test:
	go test -tags image -count=1 ./tests/image/

# One-command local bring-up: floci + state table + sample "dev" group + web console on
# http://127.0.0.1:8080/, all via `go run`. Ctrl-C stops the console; `make dev-down` stops floci.
dev:
	./scripts/dev.sh

dev-down:
	docker compose down
