#!/usr/bin/env bash
# One-command local bring-up: Floci emulator, state table, sample data, and the web console —
# all via `go run`, so code changes are picked up on the next request/re-run with no build step.
# See docs/en/development/run_local.md for the manual, per-component equivalent.
set -euo pipefail
cd "$(dirname "$0")/.."

export AWS_ENDPOINT_URL="${AWS_ENDPOINT_URL:-http://localhost:4566}"
export AWS_ACCESS_KEY_ID="${AWS_ACCESS_KEY_ID:-test}"
export AWS_SECRET_ACCESS_KEY="${AWS_SECRET_ACCESS_KEY:-test}"
export AWS_REGION="${AWS_REGION:-ap-northeast-1}"
export CHEAPSKATE_TABLE="${CHEAPSKATE_TABLE:-cheapskate-dev}"

docker compose up -d
until curl -sf "$AWS_ENDPOINT_URL/_localstack/health" > /dev/null; do sleep 1; done
echo "dev: floci ready at $AWS_ENDPOINT_URL"

go run ./cmd/dev-bootstrap

echo "dev: seeding sample group \"dev\" (idempotent)"
go run ./cmd/cheapskate-cli set-selector --group dev \
  --tag-key cheapskate:group --tag-value dev --types rds-instance,ecs-service,ec2-instance
go run ./cmd/cheapskate-cli schedule --group dev \
  -start '0 9 * * 1-5' -stop '0 21 * * 1-5' -timezone Asia/Tokyo

echo "dev: web console on http://127.0.0.1:8080/  (Ctrl-C to stop; \`make dev-down\` to stop floci)"
echo "dev: the \"dev\" group's Resources now shows two dummy ECS services (dev-cluster/api,"
echo "dev: dev-cluster/worker), tagged cheapskate:group=dev by internal/devtools/devseed; a third"
echo "dev: (dev-cluster/batch) is left untagged to show what a non-matching resource looks like."
echo "dev: There are still no dummy RDS/EC2 resources, and Floci's Resource Groups Tagging API"
echo "dev: support is otherwise limited, so those types may still show an empty list or an inline"
echo "dev: discover error — see docs/en/development/run_local.md"
exec go run ./cmd/webconsole
