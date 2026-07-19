// Package statetable creates the DynamoDB state table (pk hash key, TTL on expires_at) for tests
// and local development. Production tables are created by the operator's own IaC/deploy process
// with this same schema (docs/en/usage/setup.md §2); this package exists so that schema definition
// lives in exactly one place in Go.
package statetable

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Create creates the state table if it does not already exist (idempotent, so `make dev` can be
// re-run safely), waits for it to become active, and enables TTL on expires_at.
func Create(ctx context.Context, db *dynamodb.Client, name string) error {
	_, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            &name,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
	})
	var inUse *types.ResourceInUseException
	if err != nil && !errors.As(err, &inUse) {
		return fmt.Errorf("create table %s: %w", name, err)
	}

	waiter := dynamodb.NewTableExistsWaiter(db)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: &name}, 30*time.Second); err != nil {
		return fmt.Errorf("table %s not active: %w", name, err)
	}

	// Best-effort: TTL is enforced in code as well (store.GetOverride, store.ScanAll), so a
	// failure here (e.g. already enabled) is not fatal.
	_, _ = db.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: &name,
		TimeToLiveSpecification: &types.TimeToLiveSpecification{
			AttributeName: aws.String("expires_at"),
			Enabled:       aws.Bool(true),
		},
	})
	return nil
}
