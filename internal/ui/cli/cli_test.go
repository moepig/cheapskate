package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/app/groups"
	"cheapskate/internal/app/port"
	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	statemocks "cheapskate/internal/state/mocks"
)

func cliFixture(t *testing.T) (*statemocks.DynaStore, *groups.Service) {
	t.Helper()
	api, db := statemocks.NewDynaStore(gomock.NewController(t))
	return db, groups.New(state.New(api, "table"), porttest.NewDiscoverer(), nil, time.UTC)
}

func TestListJSONIncludesValidGroupsAndErrors(t *testing.T) {
	db, service := cliFixture(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	_, err := service.Override(context.Background(), "fine", model.OverrideRunning, 0, now)
	require.NoError(t, err)
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#broken"},
		"overide": &types.AttributeValueMemberS{Value: "running"},
	})
	var stdout bytes.Buffer

	err = cmdList(context.Background(), service, nil, &stdout, io.Discard, "json", now)
	var commandErr *commandError
	require.ErrorAs(t, err, &commandErr)
	assert.Equal(t, 2, commandErr.code)
	var output listOutput
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &output))
	require.Len(t, output.Groups, 1)
	assert.Equal(t, "fine", output.Groups[0].Name)
	require.Len(t, output.Errors, 1)
	assert.Equal(t, "broken", output.Errors[0].Group)
}

func TestListTextSeparatesValidAndInvalidRows(t *testing.T) {
	db, service := cliFixture(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	_, err := service.Override(context.Background(), "fine", model.OverrideStopped, 0, now)
	require.NoError(t, err)
	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#broken"},
	})
	var stdout, stderr bytes.Buffer

	err = cmdList(context.Background(), service, nil, &stdout, &stderr, "text", now)
	assert.Error(t, err)
	assert.Contains(t, stdout.String(), "fine")
	assert.NotContains(t, stdout.String(), "broken")
	assert.Contains(t, stderr.String(), "broken")
}

func TestMutationCommands(t *testing.T) {
	_, service := cliFixture(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()
	var output bytes.Buffer

	require.NoError(t, cmdSchedule(ctx, service, []string{"--group", "dev", "-start", "0 9 * * *", "-stop", "0 20 * * *"}, &output, "json", now))
	output.Reset()
	require.NoError(t, cmdOverride(ctx, service, []string{"--group", "dev", "disabled", "-for", "2h"}, &output, "json", now))
	assert.Contains(t, output.String(), `"override": "disabled"`)
	output.Reset()
	require.NoError(t, cmdClearOverride(ctx, service, []string{"--group", "dev"}, &output, "json", now))
	output.Reset()
	require.NoError(t, cmdRemove(ctx, service, []string{"--group", "dev"}, &output, "json"))
}

func TestOverrideForMustBePositiveWhenSpecified(t *testing.T) {
	_, service := cliFixture(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	err := cmdOverride(context.Background(), service, []string{"--group", "dev", "running", "-for", "0s"}, io.Discard, "text", now)

	assert.EqualError(t, err, "-for must be a positive duration")
}

func TestCommandsRejectUnexpectedPositionals(t *testing.T) {
	_, service := cliFixture(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	assert.Error(t, cmdSchedule(context.Background(), service, []string{"--group", "dev", "extra"}, io.Discard, "text", now))
	assert.Error(t, cmdClearOverride(context.Background(), service, []string{"--group", "dev", "extra"}, io.Discard, "text", now))
}

func TestShowJSONAndInvalidStoredGroupExitCode(t *testing.T) {
	db, service := cliFixture(t)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	_, err := service.Override(context.Background(), "dev", model.OverrideRunning, 0, now)
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	require.NoError(t, cmdShow(context.Background(), service, []string{"--group", "dev"}, &stdout, &stderr, "json", now))
	assert.Contains(t, stdout.String(), `"name": "dev"`)
	assert.Empty(t, stderr.String())

	db.Seed(map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "CONFIG"}, "sk": &types.AttributeValueMemberS{Value: "GROUP#broken"},
		"unknown": &types.AttributeValueMemberS{Value: "value"},
	})
	stdout.Reset()
	err = cmdShow(context.Background(), service, []string{"--group", "broken"}, &stdout, &stderr, "json", now)
	var commandErr *commandError
	require.ErrorAs(t, err, &commandErr)
	assert.Equal(t, 2, commandErr.code)
	assert.Contains(t, stdout.String(), `"error"`)
}

func TestShowJSONIncludesGroupAndResourceDetails(t *testing.T) {
	api, db := statemocks.NewDynaStore(gomock.NewController(t))
	store := state.New(api, "table")
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"arn:aws:ecs:service/dev/api": {
			Type: model.TypeEcsService, Ref: "dev/api", ARN: "arn:aws:ecs:service/dev/api",
			Tags: map[string]string{model.GroupTagKey: "dev", model.EcsDesiredCountTagKey: "2"},
		},
	}
	describers := map[model.ResourceType]port.Describer{
		model.TypeEcsService: porttest.Describer{Obs: model.Observation{State: model.StateRunning, Detail: "desiredCount=2"}},
	}
	service := groups.New(store, discoverer, describers, time.UTC)
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	_, err := service.Schedule(context.Background(), "dev", model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 20 * * *"}, now)
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer

	require.NoError(t, cmdShow(context.Background(), service, []string{"--group", "dev"}, &stdout, &stderr, "json", now))
	var output showOutput
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &output))
	assert.Equal(t, "dev", output.Group.Name)
	assert.Equal(t, "0 9 * * *", output.Group.StartCron)
	require.Len(t, output.Resources, 1)
	assert.Equal(t, model.TypeEcsService, output.Resources[0].Type)
	assert.Equal(t, "dev/api", output.Resources[0].Ref)
	assert.Equal(t, map[string]any{"desired_count": "2"}, output.Resources[0].Config)
	require.NotNil(t, output.Resources[0].Live)
	assert.Equal(t, model.StateRunning, output.Resources[0].Live.State)
	assert.Equal(t, "desiredCount=2", output.Resources[0].Live.Detail)
	assert.Empty(t, stderr.String())
	assert.Equal(t, 2, db.Calls("get"))
}

func TestResourceConfig(t *testing.T) {
	assert.Nil(t, resourceConfig(model.Resource{Type: model.TypeRdsInstance}))
	resource := model.Resource{Type: model.TypeEcsService, Tags: map[string]string{model.EcsDesiredCountTagKey: "2"}}
	assert.Equal(t, map[string]string{"desired_count": "2"}, resourceConfig(resource))
}
