package main

import (
	"io"
	"log/slog"
	"testing"
	"time"

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

func TestParseStatusRetention(t *testing.T) {
	tests := map[string]struct {
		raw     string
		want    time.Duration
		wantErr bool
	}{
		"default":        {raw: "", want: 30 * 24 * time.Hour},
		"custom":         {raw: "7", want: 7 * 24 * time.Hour},
		"zero":           {raw: "0", wantErr: true},
		"negative":       {raw: "-1", wantErr: true},
		"not an integer": {raw: "one month", wantErr: true},
		"too large":      {raw: "106752", wantErr: true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := parseStatusRetention(tt.raw)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
