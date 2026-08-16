// `make dev` 向けに DynamoDB の state テーブルとダミーの ECS リソース (internal/devtools/devseed) を作成する
// 冪等であり、既存のフィクスチャに対する再実行ではタグの再適用のみを行う
// Lambda のコンテナイメージには含まれない。Dockerfile がビルドするのは ./cmd/reconciler と ./cmd/webconsole である
package main

import (
	"context"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"

	"cheapskate/internal/devtools/devseed"
	"cheapskate/internal/state"
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

	if err := state.CreateTable(ctx, dynamodb.NewFromConfig(cfg), table); err != nil {
		log.Fatalf("create state table %s: %v", table, err)
	}
	log.Printf("state table %s ready", table)

	if err := devseed.Seed(ctx, ecs.NewFromConfig(cfg), resourcegroupstaggingapi.NewFromConfig(cfg)); err != nil {
		log.Fatalf("seed dummy ECS resources: %v", err)
	}
	log.Print("dummy ECS resources ready (cluster dev-cluster: api, worker, batch)")
}
