package groups

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/app/port"
	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	statemocks "cheapskate/internal/state/mocks"
)

func fixture(t *testing.T) (*statemocks.DynaStore, *Service) {
	t.Helper()
	api, db := statemocks.NewDynaStore(gomock.NewController(t))
	store := state.New(api, "table")
	return db, New(store, porttest.NewDiscoverer(), nil, time.UTC)
}

func TestScheduleCreatesAndUpdatesWithoutLosingOverride(t *testing.T) {
	_, service := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	group, err := service.Schedule(ctx, "dev", model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 20 * * *"}, now)
	require.NoError(t, err)
	assert.Equal(t, "0 9 * * *", group.StartCron)

	group, err = service.Override(ctx, "dev", model.OverrideRunning, 0, now)
	require.NoError(t, err)
	group, err = service.Schedule(ctx, "dev", model.ScheduleSpec{StartCron: "0 8 * * *", StopCron: "0 19 * * *"}, now)
	require.NoError(t, err)
	assert.Equal(t, model.OverrideRunning, group.Override)
	assert.Equal(t, "0 8 * * *", group.StartCron)
}

func TestScheduleRequiresBothCronExpressionsDespiteOverride(t *testing.T) {
	_, service := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	_, err := service.Override(ctx, "dev", model.OverrideRunning, 0, now)
	require.NoError(t, err)

	_, err = service.Schedule(ctx, "dev", model.ScheduleSpec{}, now)
	assert.ErrorContains(t, err, "start_cron and stop_cron are required")
	_, err = service.Schedule(ctx, "dev", model.ScheduleSpec{StartCron: "0 9 * * *"}, now)
	assert.ErrorContains(t, err, "start_cron and stop_cron are required")

	groups, err := service.List(ctx, now)
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Empty(t, groups[0].Group.StartCron)
	assert.Empty(t, groups[0].Group.StopCron)
}

func TestOverrideCreationAndTimedRules(t *testing.T) {
	_, service := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	group, err := service.Override(ctx, "manual", model.OverrideDisabled, 0, now)
	require.NoError(t, err)
	assert.Equal(t, model.OverrideDisabled, group.Override)

	_, err = service.Override(ctx, "missing", model.OverrideRunning, time.Hour, now)
	assert.ErrorIs(t, err, state.ErrGroupNotFound)
	_, err = service.ClearOverride(ctx, "manual", now)
	assert.ErrorContains(t, err, "schedule or override")

	_, err = service.Schedule(ctx, "scheduled", model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 20 * * *"}, now)
	require.NoError(t, err)
	group, err = service.Override(ctx, "scheduled", model.OverrideStopped, time.Hour, now)
	require.NoError(t, err)
	assert.Equal(t, now.Add(time.Hour).Unix(), group.OverrideExpiresAt)
	group, err = service.ClearOverride(ctx, "scheduled", now)
	require.NoError(t, err)
	assert.Empty(t, group.Override)
}

func TestListReturnsValidGroupsAndPerGroupErrors(t *testing.T) {
	db, service := fixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	_, err := service.Override(ctx, "fine", model.OverrideRunning, 0, now)
	require.NoError(t, err)
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#broken"},
		"overide": &types.AttributeValueMemberS{Value: "running"},
	})

	rows, err := service.List(ctx, now)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Error(t, rows[0].ConfigErr)
	assert.NoError(t, rows[1].ConfigErr)
}

func TestShowUsesFixedGroupTagAndSkipsDiscoveryForInvalidConfig(t *testing.T) {
	api, db := statemocks.NewDynaStore(gomock.NewController(t))
	store := state.New(api, "table")
	ctx := context.Background()
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"a": {Type: model.TypeRdsInstance, Ref: "dev-db", ARN: "a", Tags: map[string]string{model.GroupTagKey: "dev"}},
		"b": {Type: model.TypeRdsInstance, Ref: "other-db", ARN: "b", Tags: map[string]string{model.GroupTagKey: "other"}},
	}
	describers := map[model.ResourceType]port.Describer{
		model.TypeRdsInstance: porttest.Describer{Obs: model.Observation{State: model.StateRunning}},
	}
	service := New(store, discoverer, describers, time.UTC)
	_, err := service.Override(ctx, "dev", model.OverrideRunning, 0, now)
	require.NoError(t, err)

	detail, err := service.Show(ctx, "dev", now)
	require.NoError(t, err)
	require.Len(t, detail.Resources, 1)
	assert.Equal(t, "dev-db", detail.Resources[0].Resource.Ref)
	assert.Equal(t, 1, discoverer.Calls())

	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#broken"},
		"unknown": &types.AttributeValueMemberS{Value: "value"},
	})
	_, err = service.Show(ctx, "broken", now)
	assert.ErrorIs(t, err, ErrInvalidConfig)
	assert.Equal(t, 1, discoverer.Calls())
}
