package cloudwatch

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEmitter(enabled bool) (Emitter, *bytes.Buffer) {
	var buf bytes.Buffer
	return Emitter{Log: slog.New(slog.NewJSONHandler(&buf, nil)), Namespace: "cheapskate", Enabled: enabled}, &buf
}

// EMF は形が合っていないと CloudWatch がメトリクスを生成せず、しかもエラーにもならず静かに無視される
// そのため検証すべきは「ログが出たこと」ではなく、_aws ブロックの構造そのものである
func TestEmitProducesValidEMF(t *testing.T) {
	e, buf := newEmitter(true)
	at := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)

	e.Emit(at, []Metric{{Name: "ReconcileErrors", Value: 2}, {Name: "ReconcileActions", Value: 0}})

	var got map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))

	// 値は _aws の外に、メトリクス名そのものをキーとして置かれていなければならない
	assert.Equal(t, float64(2), got["ReconcileErrors"])
	assert.Equal(t, float64(0), got["ReconcileActions"], "値が 0 のメトリクスも省略せず出す（0 も観測結果である）")

	aws, ok := got["_aws"].(map[string]any)
	require.True(t, ok, "_aws block missing")
	assert.Equal(t, float64(at.UnixMilli()), aws["Timestamp"])

	directives := aws["CloudWatchMetrics"].([]any)
	require.Len(t, directives, 1)
	d := directives[0].(map[string]any)
	assert.Equal(t, "cheapskate", d["Namespace"])
	assert.Equal(t, []any{[]any{}}, d["Dimensions"], "次元なしの集計メトリクスとして出す")
	assert.Equal(t, []any{
		map[string]any{"Name": "ReconcileErrors", "Unit": "Count"},
		map[string]any{"Name": "ReconcileActions", "Unit": "Count"},
	}, d["Metrics"])
}

// METRICS_ENABLED=false に相当する状態では 1 バイトも出さない
// メトリクスを切った利用者のロググループに、CloudWatch が捨てるだけの EMF 行を溜めないため
func TestDisabledEmitterWritesNothing(t *testing.T) {
	e, buf := newEmitter(false)

	e.Emit(time.Now(), []Metric{{Name: "ReconcileErrors", Value: 1}})

	assert.Empty(t, buf.String())
}

// 無効化の判断は Emitter が持つ
// 発行箇所ごとに if を書かせると片方だけ書き忘れる余地が残るので、そこを型で閉じている
func TestDisabledEmitterStaysSilentAcrossRepeatedCalls(t *testing.T) {
	e, buf := newEmitter(false)

	for range 3 {
		e.Emit(time.Now(), []Metric{{Name: "ReconcileAborted", Value: 1}})
	}

	assert.Empty(t, buf.String())
}

// 名前空間が空なら出さない
// 呼び出し側が既定値を埋め忘れた場合であり、CloudWatch 側では捨てられるだけの行になる
func TestEmitWithEmptyNamespaceWritesNothing(t *testing.T) {
	var buf bytes.Buffer
	e := Emitter{Log: slog.New(slog.NewJSONHandler(&buf, nil)), Namespace: "", Enabled: true}

	e.Emit(time.Now(), []Metric{{Name: "ReconcileErrors", Value: 1}})

	assert.Empty(t, buf.String())
}

// メトリクス名を 1 つも渡さなかった場合も、空の CloudWatchMetrics ディレクティブを書かない
// 呼び出し側が動的にメトリクスを組み立てたときに、結果が空になっても壊れないようにする
func TestEmitWithNoMetricsWritesNothing(t *testing.T) {
	e, buf := newEmitter(true)

	e.Emit(time.Now(), nil)

	assert.Empty(t, buf.String())
}
