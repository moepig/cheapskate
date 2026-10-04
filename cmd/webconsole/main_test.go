package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 未設定時は待ち受けポートに対応するループバックの Host だけを許可し、明示指定時はそのリストで置き換えることを確認する。
func TestAllowedHosts(t *testing.T) {
	for _, test := range []struct {
		name   string
		raw    string
		listen string
		want   []string
	}{
		{"local default", "", "127.0.0.1:8080", []string{"localhost:8080", "127.0.0.1:8080", "[::1]:8080"}},
		{"container port", "", "127.0.0.1:8000", []string{"localhost:8000", "127.0.0.1:8000", "[::1]:8000"}},
		{"custom listen", "", "0.0.0.0:9090", []string{"localhost:9090", "127.0.0.1:9090", "[::1]:9090"}},
		{"external hosts", " console.example, api.example:443, ", "127.0.0.1:8000", []string{"console.example", "api.example:443"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			hosts, err := allowedHosts(test.raw, test.listen)
			require.NoError(t, err)
			assert.Equal(t, test.want, hosts)
		})
	}
}

// 空白と区切りだけの許可リスト、および既定ホストのポートを決定できない待ち受けアドレスを拒否することを確認する。
func TestInvalidAllowedHosts(t *testing.T) {
	for _, test := range []struct {
		raw    string
		listen string
	}{
		{" ", "127.0.0.1:8080"},
		{", ,", "127.0.0.1:8080"},
		{"", "invalid"},
	} {
		hosts, err := allowedHosts(test.raw, test.listen)
		assert.Error(t, err)
		assert.Empty(t, hosts)
	}
}
