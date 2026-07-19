#!/usr/bin/env bash
# A-1: container smoke test. Builds the image, runs it under the Lambda Runtime Interface
# Emulator (bundled in public.ecr.aws/lambda/provided:al2023 as /usr/local/bin/aws-lambda-rie),
# and checks both entrypoints actually respond:
#   - the default entrypoint (/var/runtime/bootstrap, the reconciler) via a `{}` invoke
#   - the opt-in entrypoint (/var/runtime/webconsole, selected in prod via ImageConfig.EntryPoint)
#     via a minimal API Gateway proxy event
#
# Requires: docker, a running floci emulator (`make floci-up`) on the "cheapskate_default"
# network so the container can reach DynamoDB at http://floci:4566.
set -euo pipefail

IMAGE="${IMAGE:-cheapskate}"
TAG="${TAG:-smoke}"
NETWORK="${NETWORK:-cheapskate_default}"
ENDPOINT="${AWS_ENDPOINT_URL:-http://localhost:4566}"

export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export AWS_REGION=ap-northeast-1
export AWS_ENDPOINT_URL="$ENDPOINT"

if ! curl -sf "$ENDPOINT/_localstack/health" > /dev/null; then
  echo "smoke: no emulator at $ENDPOINT (start one with \`make floci-up\`)" >&2
  exit 1
fi

TABLE="cheapskate-smoke-$$"
cleanup() {
  aws dynamodb delete-table --table-name "$TABLE" --endpoint-url "$ENDPOINT" > /dev/null 2>&1 || true
  docker rm -f smoke-reconciler smoke-webconsole > /dev/null 2>&1 || true
}
trap cleanup EXIT

echo "smoke: building image $IMAGE:$TAG"
docker build --platform linux/amd64 -t "$IMAGE:$TAG" "$(dirname "$0")/.."

echo "smoke: creating table $TABLE"
aws dynamodb create-table \
  --table-name "$TABLE" \
  --attribute-definitions AttributeName=pk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST \
  --endpoint-url "$ENDPOINT" > /dev/null
aws dynamodb wait table-exists --table-name "$TABLE" --endpoint-url "$ENDPOINT"

# --- reconciler entrypoint (default: /var/runtime/bootstrap) ---
echo "smoke: starting reconciler under RIE"
docker run -d --rm --name smoke-reconciler \
  --network "$NETWORK" \
  -p 9000:8080 \
  -e STATE_TABLE_NAME="$TABLE" \
  -e AWS_ENDPOINT_URL="http://floci:4566" \
  -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test -e AWS_REGION=ap-northeast-1 \
  --entrypoint /usr/local/bin/aws-lambda-rie \
  "$IMAGE:$TAG" /var/runtime/bootstrap > /dev/null

for i in $(seq 1 30); do
  if curl -sf -X POST "http://localhost:9000/2015-03-31/functions/function/invocations" -d '{}' > /tmp/smoke-reconciler.json 2>/dev/null; then
    break
  fi
  sleep 1
done
if ! grep -q '"reconciled"' /tmp/smoke-reconciler.json; then
  echo "smoke: reconciler invoke did not return a Summary:" >&2
  cat /tmp/smoke-reconciler.json >&2
  exit 1
fi
echo "smoke: reconciler OK: $(cat /tmp/smoke-reconciler.json)"
docker rm -f smoke-reconciler > /dev/null

# --- webconsole entrypoint (opt-in override: /var/runtime/webconsole) ---
echo "smoke: starting webconsole under RIE"
docker run -d --rm --name smoke-webconsole \
  --network "$NETWORK" \
  -p 9001:8080 \
  -e STATE_TABLE_NAME="$TABLE" \
  -e AWS_ENDPOINT_URL="http://floci:4566" \
  -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test -e AWS_REGION=ap-northeast-1 \
  --entrypoint /usr/local/bin/aws-lambda-rie \
  "$IMAGE:$TAG" /var/runtime/webconsole > /dev/null

event='{"resource":"/","path":"/","httpMethod":"GET","headers":{"Host":"example.com"},"multiValueHeaders":{},"queryStringParameters":null,"multiValueQueryStringParameters":null,"pathParameters":null,"stageVariables":null,"requestContext":{"accountId":"123456789012","resourcePath":"/","path":"/","httpMethod":"GET","requestId":"smoke","stage":"smoke","identity":{}},"body":null,"isBase64Encoded":false}'
for i in $(seq 1 30); do
  if curl -sf -X POST "http://localhost:9001/2015-03-31/functions/function/invocations" -d "$event" > /tmp/smoke-webconsole.json 2>/dev/null; then
    break
  fi
  sleep 1
done
if ! grep -q '"statusCode":200' /tmp/smoke-webconsole.json; then
  echo "smoke: webconsole invoke did not return HTTP 200:" >&2
  cat /tmp/smoke-webconsole.json >&2
  exit 1
fi
echo "smoke: webconsole OK"
docker rm -f smoke-webconsole > /dev/null

echo "smoke: all entrypoints responded"
