//go:build integration

package state_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
	"cheapskate/internal/devtools/emutest"
	"cheapskate/internal/state"
)

func newStore(t *testing.T) *state.Store {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	return state.New(dynamodb.NewFromConfig(cfg), table)
}

func TestGroupRoundtrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	got, err := s.GetGroup(ctx, "dev")
	require.NoError(t, err)
	assert.Nil(t, got)

	item := model.GroupSpec{
		Name: "dev", Mode: model.ModeSchedule, StartCron: "0 9 * * 1-5", StopCron: "0 21 * * 1-5",
		TagKey: "cheapskate:group", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance, model.TypeEcsService},
	}
	require.NoError(t, s.PutGroup(ctx, item))

	got, err = s.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, model.ModeSchedule, got.Mode)
	assert.ElementsMatch(t, []model.ResourceType{model.TypeRdsInstance, model.TypeEcsService}, got.Types)
}

func TestOverrideExpiryEnforcedInCode(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, s.PutOverride(ctx, "dev", model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(time.Hour).Unix()}))
	o, err := s.GetOverride(ctx, "dev", now)
	require.NoError(t, err)
	require.NotNil(t, o)
	assert.Equal(t, model.DesiredRunning, o.Desired)

	// TTL による削除は遅延するため、store は過去の expires_at を持つ override を存在しないものとして扱わなければならない
	o, err = s.GetOverride(ctx, "dev", now.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Nil(t, o, "expired override must be nil")
}

func TestStatusRoundtrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	err := s.UpdateStatus(ctx, "ecs-service#dev/api", state.StatusPatch{
		LastAction:    state.Set(model.ActionStop),
		ObservedState: state.Set(model.StateRunning),
		// nil のフィールドは、対応する属性を変更してはならない
	})
	require.NoError(t, err)
	// 2 回目の部分更新は、置き換えではなく統合でなければならない
	require.NoError(t, s.UpdateStatus(ctx, "ecs-service#dev/api", state.StatusPatch{LastAction: state.Set(model.ActionStart)}))

	status, err := s.GetStatus(ctx, "ecs-service#dev/api")
	require.NoError(t, err)
	assert.Equal(t, model.ActionStart, status.LastAction)
	assert.Equal(t, model.StateRunning, status.ObservedState, "the earlier partial update's field must survive the merge")
}

// グループ単位の失敗である設定エラーと Discover の失敗は、合成 resource_id である "group#<name>" の下に記録する
// これにより、実リソースと同じ status アイテムの形状と API を共有する
func TestGroupStatusRoundtrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	require.NoError(t, s.UpdateStatus(ctx, model.GroupStatusID("dev"), state.StatusPatch{LastError: state.Set("discover: access denied")}))
	status, err := s.GetStatus(ctx, model.GroupStatusID("dev"))
	require.NoError(t, err)
	assert.Equal(t, "discover: access denied", status.LastError)
}

func TestScanAllJoinsGroupAndOverrideAgainstRealDynamoDB(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, s.PutGroup(ctx, model.GroupSpec{Name: "dev", Mode: model.ModePinned, Desired: model.DesiredStopped}))
	require.NoError(t, s.PutOverride(ctx, "dev", model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(time.Hour).Unix()}))
	require.NoError(t, s.UpdateStatus(ctx, "rds-instance#a", state.StatusPatch{LastAction: state.Set(model.ActionStop)}))

	res, err := s.ScanAll(ctx, now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	assert.True(t, res.Groups[0].HasGroup)
	require.NotNil(t, res.Groups[0].Override)
	assert.Equal(t, model.DesiredRunning, res.Groups[0].Override.Desired)
	require.Contains(t, res.Statuses, "rds-instance#a")
	assert.Equal(t, model.ActionStop, res.Statuses["rds-instance#a"].LastAction)
}
