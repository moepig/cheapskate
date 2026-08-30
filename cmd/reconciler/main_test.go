package main

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMetricsAreDisabledByDefault(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "")

	emitter := metricsEmitter(slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.False(t, emitter.Enabled)
	assert.Equal(t, defaultMetricsNamespace, emitter.Namespace)
}

func TestMetricsCanBeExplicitlyEnabled(t *testing.T) {
	t.Setenv("METRICS_ENABLED", "true")
	t.Setenv("METRICS_NAMESPACE", "custom")

	emitter := metricsEmitter(slog.New(slog.NewTextHandler(io.Discard, nil)))

	assert.True(t, emitter.Enabled)
	assert.Equal(t, "custom", emitter.Namespace)
}
