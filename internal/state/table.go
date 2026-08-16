package state

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// state テーブルが存在しない場合に作成する。pk をハッシュキー、expires_at を TTL とする
// 冪等であり、`make dev` の再実行に対応する
// 作成後はテーブルが active となるまで待機し、expires_at の TTL を有効化する
func CreateTable(ctx context.Context, db *dynamodb.Client, name string) error {
	_, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            &name,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
	})
	if _, ok := errors.AsType[*types.ResourceInUseException](err); err != nil && !ok {
		return fmt.Errorf("create table %s: %w", name, err)
	}

	waiter := dynamodb.NewTableExistsWaiter(db)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: &name}, 30*time.Second); err != nil {
		return fmt.Errorf("table %s not active: %w", name, err)
	}

	// ベストエフォートで実行する
	// TTL の判定は state.GetOverride と state.ScanAll でも行うため、ここでの失敗は動作に影響しない
	_, _ = db.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: &name,
		TimeToLiveSpecification: &types.TimeToLiveSpecification{
			AttributeName: aws.String("expires_at"),
			Enabled:       aws.Bool(true),
		},
	})
	return nil
}
