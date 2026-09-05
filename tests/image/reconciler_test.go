//go:build image

// ビルド済みの reconciler イメージへ、定期呼び出しと任意の JSON ペイロードを投入する
//
// 検証の対象はイメージの振る舞いであり、RIE と testcontainers は実行の手段である
// パッケージの位置づけとハーネスの前提は、doc.go と harness_test.go を参照
//
//	make image-test   # = go test -tags image -count=1 ./tests/image/
package image

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/app/reconcile"
	"cheapskate/internal/devtools/emutest"
)

func TestReconcilerImageHandlesEventPayloads(t *testing.T) {
	cfg := emutest.Config(t)
	// 使い捨ての空テーブルであるため、期待する応答はグループなし、すなわちリソース 0 件に固定できる
	table := emutest.CreateStateTable(t, cfg)

	// 空テーブルへの {} は状態を変更しないため、warm up に用いる (後続のログの集計にも影響しない)
	reconciler := startUnderRIE(t, buildImage(t, "reconciler"), emulatorEnv(t, table), []byte("{}"))

	// {} は定期トリガと手動実行のペイロードである (setup.md §8)
	t.Run("periodic payload", func(t *testing.T) {
		var summary reconcile.Summary
		require.NoError(t, json.Unmarshal(reconciler.invoke(t, []byte("{}")), &summary))
		assert.Equal(t, 0, summary.Reconciled)
		assert.Empty(t, summary.Actions)
		assert.Empty(t, summary.Errors)
	})

	// payload の内容は解釈せず、どの呼び出しでも full reconcile を実行する。
	t.Run("arbitrary payload", func(t *testing.T) {
		var summary reconcile.Summary
		require.NoError(t, json.Unmarshal(reconciler.invoke(t, []byte("[]")), &summary))
		assert.Equal(t, 0, summary.Reconciled)
		assert.Empty(t, summary.Errors)
	})
}

func TestReconcilerImageRejectsInvalidTimezone(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	assertRejectsInvalidTimezone(t, buildImage(t, "reconciler"), emulatorEnv(t, table))
}
