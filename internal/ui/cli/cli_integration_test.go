//go:build integration

package cli

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
	"cheapskate/internal/devtools/emutest"
	"cheapskate/internal/state"
)

func TestCLILifecycleAgainstDynamoDB(t *testing.T) {
	t.Setenv("CHEAPSKATE_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	store := state.New(dynamodb.NewFromConfig(cfg), table)
	require.NoError(t, Run([]string{"config", "set-context", "test", "--table", table, "--region", cfg.Region, "--timezone", "UTC"}, io.Discard))
	require.NoError(t, Run([]string{"config", "use-context", "test"}, io.Discard))
	args := func(values ...string) []string {
		return append([]string{"-output", "json"}, values...)
	}

	require.NoError(t, Run(args("schedule", "--group", "dev", "-start", "0 9 * * *", "-stop", "0 20 * * *"), io.Discard))
	require.NoError(t, Run(args("override", "--group", "dev", "running", "-for", "2h"), io.Discard))
	group, err := store.GetGroup(context.Background(), "dev")
	require.NoError(t, err)
	require.NotNil(t, group)
	assert.Equal(t, model.OverrideRunning, group.Override)
	assert.NotZero(t, group.OverrideExpiresAt)

	require.NoError(t, Run(args("clear-override", "--group", "dev"), io.Discard))
	require.NoError(t, Run(args("remove", "--group", "dev"), io.Discard))
	group, err = store.GetGroup(context.Background(), "dev")
	require.NoError(t, err)
	assert.Nil(t, group)
}
