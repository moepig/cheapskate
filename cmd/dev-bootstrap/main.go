// dev-bootstrap creates the DynamoDB state table for `make dev`. It is idempotent (re-running it
// against an existing table is a no-op) and is never built into the Lambda container image — the
// Dockerfile builds only ./cmd/reconciler and ./cmd/webconsole.
package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"cheapskate/internal/statetable"
)

func main() {
	table := os.Getenv("CHEAPSKATE_TABLE")
	if table == "" {
		log.Fatal("CHEAPSKATE_TABLE is required")
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("load AWS config: %v", err)
	}

	if err := statetable.Create(ctx, dynamodb.NewFromConfig(cfg), table); err != nil {
		log.Fatalf("create state table %s: %v", table, err)
	}
	log.Printf("state table %s ready", table)
}
