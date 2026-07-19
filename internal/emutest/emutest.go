// Package emutest wires integration tests to a local AWS emulator (Floci). Connectivity uses only the standard AWS_ENDPOINT_URL environment variable; production code is emulator-unaware.
package emutest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const defaultEndpoint = "http://localhost:4566"

// Config returns an AWS config pointed at the emulator, or skips the test when no emulator is reachable.
func Config(t *testing.T) aws.Config {
	t.Helper()
	endpoint := os.Getenv("AWS_ENDPOINT_URL")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(endpoint + "/_localstack/health")
	if err != nil {
		t.Skipf("no AWS emulator at %s (start one with `make floci-up`): %v", endpoint, err)
	}
	resp.Body.Close()

	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_REGION", "ap-northeast-1")

	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		t.Fatalf("load AWS config: %v", err)
	}
	return cfg
}

// RandomName returns a unique name with the given prefix.
func RandomName(prefix string) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// CreateStateTable creates a throwaway state table (PK pk, TTL expires_at) and registers its deletion as cleanup.
func CreateStateTable(t *testing.T, cfg aws.Config) string {
	t.Helper()
	ctx := context.Background()
	db := dynamodb.NewFromConfig(cfg)
	name := RandomName("cheapskate-itest")

	_, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            &name,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: &name})
	})

	waiter := dynamodb.NewTableExistsWaiter(db)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: &name}, 30*time.Second); err != nil {
		t.Fatalf("table not active: %v", err)
	}
	// TTL is enforced in code as well; enabling it here just mirrors production.
	_, _ = db.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: &name,
		TimeToLiveSpecification: &types.TimeToLiveSpecification{
			AttributeName: aws.String("expires_at"),
			Enabled:       aws.Bool(true),
		},
	})
	return name
}
