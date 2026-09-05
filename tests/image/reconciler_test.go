//go:build image

// ビルド済みの reconciler イメージへ、定期呼び出しと任意の JSON ペイロードを投入する
//
// 検証の対象はイメージの振る舞いであり、RIE と testcontainers は実行の手段である
// パッケージの位置づけとハーネスの前提は、doc.go と harness_test.go を参照
//
//	make image-test   # = go test -tags image -count=1 ./tests/image/
package image

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/app/reconcile"
	"cheapskate/internal/devtools/emutest"
)

func TestReconcilerImageHandlesEventPayloads(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	_, err := dynamodb.NewFromConfig(cfg).PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(table),
		Item: map[string]types.AttributeValue{
			"pk":      &types.AttributeValueMemberS{Value: "CONFIG"},
			"sk":      &types.AttributeValueMemberS{Value: "GROUP#broken"},
			"unknown": &types.AttributeValueMemberS{Value: "value"},
		},
	})
	require.NoError(t, err)

	reconciler := startUnderRIE(t, buildImage(t, "reconciler"), emulatorEnv(t, table), []byte("{}"))

	t.Run("periodic payload", func(t *testing.T) {
		summary := requireSummaryResponse(t, reconciler.invoke(t, []byte("{}")))
		assert.Equal(t, 0, summary.Reconciled)
		assert.Empty(t, summary.Actions)
		require.Len(t, summary.Errors, 1)
		assert.Equal(t, "broken", summary.Errors[0].Group)
	})

	t.Run("arbitrary payload", func(t *testing.T) {
		summary := requireSummaryResponse(t, reconciler.invoke(t, []byte("[]")))
		assert.Equal(t, 0, summary.Reconciled)
		require.Len(t, summary.Errors, 1)
	})
}

func requireSummaryResponse(t *testing.T, body []byte) reconcile.Summary {
	t.Helper()
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &raw))
	assert.NotContains(t, raw, "errorMessage")
	assert.NotContains(t, raw, "errorType")
	require.Contains(t, raw, "reconciled")
	require.Contains(t, raw, "actions")
	require.Contains(t, raw, "errors")

	var summary reconcile.Summary
	require.NoError(t, json.Unmarshal(body, &summary))
	return summary
}

func TestReconcilerImageRejectsInvalidTimezone(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	assertRejectsInvalidTimezone(t, buildImage(t, "reconciler"), emulatorEnv(t, table))
}
