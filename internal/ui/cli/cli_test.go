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

func TestResourceConfig(t *testing.T) {
	assert.Nil(t, resourceConfig(model.Resource{Type: model.TypeRdsInstance}))
	resource := model.Resource{Type: model.TypeEcsService, Tags: map[string]string{model.EcsDesiredCountTagKey: "2"}}
	assert.Equal(t, map[string]string{"desired_count": "2"}, resourceConfig(resource))
}
