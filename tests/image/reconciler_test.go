//go:build image

// ビルド済みの reconciler イメージに、本番で EventBridge が届けるのと同じペイロードを投げる
//
// 検証するのはイメージの振る舞いであって、RIE でも testcontainers でもない（それらは動かすための道具）
// パッケージの位置づけとハーネスの前提は doc.go と harness_test.go を参照
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

// イベントフィクスチャの位置（リポジトリルートから見た相対）
// フィクスチャは単体テストと共用で、EventBridge ルールパターンの参照ペイロードも兼ねる
const fixtureDir = repoRoot + "/internal/app/reconcile/testdata"

// ハンドラが error を返したときに Lambda ランタイムが返すペイロード
type lambdaError struct {
	ErrorMessage string `json:"errorMessage"`
	ErrorType    string `json:"errorType"`
}

func TestReconcilerImageHandlesEventPayloads(t *testing.T) {
	cfg := emutest.Config(t)
	// 使い捨ての空テーブルなので、期待する応答は「グループなし = リソース 0 件」に固定できる
	table := emutest.CreateStateTable(t, cfg)

	// 空テーブルへの {} は無害なので、そのまま warm up に使える（後段のログの数え上げにも影響しない）
	reconciler := startUnderRIE(t, buildImage(t, "reconciler"), emulatorEnv(t, table), []byte("{}"))

	// {} は定期トリガと手動実行のペイロードである（setup.md §8）
	t.Run("periodic payload", func(t *testing.T) {
		var summary reconcile.Summary
		require.NoError(t, json.Unmarshal(reconciler.invoke(t, []byte("{}")), &summary))
		assert.Equal(t, 0, summary.Reconciled)
		assert.Empty(t, summary.Actions)
		assert.Empty(t, summary.Errors)
	})

	// EventBridge が届けるイベント本体をそのまま送る
	// glob なのでフィクスチャを増やせばここも自動で対象になる
	fixtures, err := filepath.Glob(filepath.Join(fixtureDir, "rds-event-*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, fixtures, "no RDS event fixtures found in "+fixtureDir)

	for _, fixture := range fixtures {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			payload, err := os.ReadFile(fixture)
			require.NoError(t, err)

			// RDS イベントもフル reconcile を起こす（イベントが名指しするリソースだけを見に行くことはない）
			var summary reconcile.Summary
			require.NoError(t, json.Unmarshal(reconciler.invoke(t, payload), &summary))
			assert.Equal(t, 0, summary.Reconciled)
			assert.Empty(t, summary.Errors)
		})
	}

	// 応答だけでは「受理された」までしか分からない
	// aws.rds のイベントとして解釈されたことは、ハンドラが残すログでしか確認できない
	t.Run("rds events were recognised", func(t *testing.T) {
		assert.Equal(t, len(fixtures),
			strings.Count(reconciler.logs(t), `"msg":"event-received","source":"aws.rds"`),
			"expected one event-received log line per RDS fixture")
	})

	// オブジェクトですらないペイロードは、空のイベントとみなして reconcile を続けるのではなく失敗する
	t.Run("malformed payload", func(t *testing.T) {
		var failure lambdaError
		require.NoError(t, json.Unmarshal(reconciler.invoke(t, []byte("[]")), &failure))
		assert.Contains(t, failure.ErrorMessage, "unmarshal event")
	})
}
