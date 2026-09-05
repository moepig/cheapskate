package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMetricsAreDisabledByDefault(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "")

	emitter, err := metricsEmitter(slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.NoError(t, err)
	assert.False(t, emitter.Enabled)
	assert.Equal(t, defaultMetricsNamespace, emitter.Namespace)
}

func TestMetricsCanBeExplicitlyEnabled(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "true")
	t.Setenv("METRICS_NAMESPACE", "custom")

	emitter, err := metricsEmitter(slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.NoError(t, err)
	assert.True(t, emitter.Enabled)
	assert.Equal(t, "custom", emitter.Namespace)
}

func TestInvalidMetricsSettingIsRejected(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "sometimes")

	_, err := metricsEmitter(slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.ErrorContains(t, err, "invalid METRICS_ENABLED")
}
