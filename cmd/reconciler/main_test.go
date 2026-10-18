package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"cheapskate/internal/aws/cloudwatch"
)

func TestMetricsAreDisabledByDefault(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "")

	emitter, err := metricsEmitter(io.Discard)

	assert.NoError(t, err)
	assert.False(t, emitter.Enabled)
	assert.Equal(t, defaultMetricsNamespace, emitter.Namespace)
}

func TestMetricsCanBeExplicitlyEnabled(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "true")
	t.Setenv("METRICS_NAMESPACE", "custom")

	emitter, err := metricsEmitter(io.Discard)

	assert.NoError(t, err)
	assert.True(t, emitter.Enabled)
	assert.Equal(t, "custom", emitter.Namespace)
}

func TestInvalidMetricsSettingIsRejected(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "sometimes")

	_, err := metricsEmitter(io.Discard)

	assert.ErrorContains(t, err, "invalid METRICS_ENABLED")
}

// WARN と ERROR の通常ログ設定でも、有効な EMF メトリクスが出力されることを確認する。
func TestMetricsIgnoreLogLevel(t *testing.T) {
	for _, level := range []string{"warn", "error"} {
		t.Run(level, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", level)
			t.Setenv("METRICS_ENABLED", "true")
			var output bytes.Buffer
			emitter, err := metricsEmitter(&output)
			assert.NoError(t, err)

			emitter.Emit(time.Now(), []cloudwatch.Metric{{Name: "ReconcileAborted", Value: 1}})

			var record map[string]any
			if assert.NoError(t, json.Unmarshal(output.Bytes(), &record)) {
				assert.Equal(t, float64(1), record["ReconcileAborted"])
				assert.Contains(t, record, "_aws")
			}
		})
	}
}

func TestLogLevel(t *testing.T) {
	for _, test := range []struct {
		value string
		want  slog.Level
	}{
		{"", slog.LevelInfo},
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
	} {
		t.Run("level="+test.value, func(t *testing.T) {
			t.Setenv("LOG_LEVEL", test.value)

			level, err := logLevel()

			assert.NoError(t, err)
			assert.Equal(t, test.want, level)
		})
	}
}

func TestInvalidLogLevelIsRejected(t *testing.T) {
	t.Setenv("LOG_LEVEL", "verbose")

	_, err := logLevel()

	assert.ErrorContains(t, err, "invalid LOG_LEVEL")
}
