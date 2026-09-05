//go:build integration

package state_test

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
	"cheapskate/internal/devtools/emutest"
	"cheapskate/internal/state"
)

func TestGroupOperationsAgainstDynamoDB(t *testing.T) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	store := state.New(dynamodb.NewFromConfig(cfg), table)
	ctx := context.Background()

	group := model.GroupSpec{Name: "dev", StartCron: "0 9 * * *", StopCron: "0 20 * * *"}
	require.NoError(t, store.CreateGroup(ctx, group))
	require.NoError(t, store.SetOverride(ctx, "dev", model.OverrideRunning, 0))
	require.NoError(t, store.SetSchedule(ctx, "dev", model.ScheduleSpec{StartCron: "0 8 * * *", StopCron: "0 19 * * *"}))

	got, err := store.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, model.OverrideRunning, got.Override)
	assert.Equal(t, "0 8 * * *", got.StartCron)

	require.NoError(t, store.ClearOverride(ctx, "dev"))
	require.NoError(t, store.DeleteGroup(ctx, "dev"))
	got, err = store.GetGroup(ctx, "dev")
	require.NoError(t, err)
	assert.Nil(t, got)
}
