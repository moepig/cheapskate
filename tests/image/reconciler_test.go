//go:build image

// ビルド済みの reconciler イメージへ、本番で EventBridge が送信するものと同じペイロードを投入する
//
// 検証の対象はイメージの振る舞いであり、RIE と testcontainers は実行の手段である
// パッケージの位置づけとハーネスの前提は、doc.go と harness_test.go を参照
//
//	make image-test   # = go test -tags image -count=1 ./tests/image/
package image

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/app/reconcile"
	"cheapskate/internal/devtools/emutest"
)

// イベントフィクスチャの位置 (リポジトリルートからの相対)
// フィクスチャは単体テストと共用であり、EventBridge ルールパターンの参照ペイロードを兼ねる
const fixtureDir = repoRoot + "/internal/app/reconcile/testdata"

// ハンドラが error を返した場合に Lambda ランタイムが返すペイロード
type lambdaError struct {
	ErrorMessage string `json:"errorMessage"`
	ErrorType    string `json:"errorType"`
}

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

	// EventBridge が送信するイベント本体をそのまま投入する
	// glob により取得するため、フィクスチャの追加は自動で対象となる
	fixtures, err := filepath.Glob(filepath.Join(fixtureDir, "rds-event-*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, fixtures, "no RDS event fixtures found in "+fixtureDir)

	for _, fixture := range fixtures {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			payload, err := os.ReadFile(fixture)
			require.NoError(t, err)

			// RDS イベントもフル reconcile を実行する (イベントが指定するリソースのみを対象とはしない)
			var summary reconcile.Summary
			require.NoError(t, json.Unmarshal(reconciler.invoke(t, payload), &summary))
			assert.Equal(t, 0, summary.Reconciled)
			assert.Empty(t, summary.Errors)
		})
	}

	// 応答から判定できるのは、呼び出しの受理までである
	// aws.rds のイベントとして解釈したことは、ハンドラが出力するログでのみ確認できる
	t.Run("rds events were recognised", func(t *testing.T) {
		assert.Equal(t, len(fixtures),
			strings.Count(reconciler.logs(t), `"msg":"event-received","source":"aws.rds"`),
			"expected one event-received log line per RDS fixture")
	})

	// JSON オブジェクトでないペイロードは、空のイベントとして reconcile を継続せず、失敗とする
	t.Run("malformed payload", func(t *testing.T) {
		var failure lambdaError
		require.NoError(t, json.Unmarshal(reconciler.invoke(t, []byte("[]")), &failure))
		assert.Contains(t, failure.ErrorMessage, "unmarshal event")
	})
}
