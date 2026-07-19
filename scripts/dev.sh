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

echo "dev: seeding sample tag \"dev\" (idempotent)"
go run ./cmd/cheapskate-cli add --tag dev --type rds-instance --name dev-db
go run ./cmd/cheapskate-cli add --tag dev --type ecs --cluster dev-cluster --service api -restore-count 2
go run ./cmd/cheapskate-cli schedule --tag dev \
  -start '0 9 * * 1-5' -stop '0 21 * * 1-5' -timezone Asia/Tokyo

echo "dev: web console on http://127.0.0.1:8080/  (Ctrl-C to stop; \`make dev-down\` to stop floci)"
echo "dev: seeded resources have no real RDS/ECS backing in Floci, so the reconciler will report them as not-found — see docs/en/development/run_local.md"
exec go run ./cmd/webconsole
