//go:build integration

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
	"cheapskate/internal/devtools/emutest"
	"cheapskate/internal/state"
)

// JSON 出力を破棄して CLI を実行する
// ここでのテストは結果として残る DynamoDB のアイテムを検証し、出力の形は main_test.go の単体テストが担保する
func runq(args []string) error { return Run(args, io.Discard) }

func setup(t *testing.T) (*state.Store, string) {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	return state.New(dynamodb.NewFromConfig(cfg), table), table
}

func TestSetSelectorPinScheduleDisableRemoveLifecycle(t *testing.T) {
	s, table := setup(t)
	ctx := context.Background()
	args := func(a ...string) []string { return append([]string{"-table", table}, a...) }

	require.NoError(t, runq(args("set-selector", "--group", "dev", "--tag-key", "env", "--tag-value", "dev", "--types", "rds-cluster")))
	require.NoError(t, runq(args("pin", "--group", "dev", "stopped")))
	group, err := s.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, group)
	assert.Equal(t, model.ModePinned, group.Mode)
	assert.Equal(t, model.DesiredStopped, group.Desired)
	assert.Equal(t, "env", group.TagKey)
	assert.Equal(t, []model.ResourceType{model.TypeRdsCluster}, group.Types)

	// set-selector を再実行すると、セレクタの types を更新しつつ既存の mode と desired は read-modify-write で保たれなければならない
	require.NoError(t, runq(args("set-selector", "--group", "dev", "--tag-key", "env", "--tag-value", "dev", "--types", "rds-cluster,ecs-service")))
	group, err = s.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, group)
	assert.Equal(t, model.ModePinned, group.Mode, "set-selector must not reset mode")
	assert.ElementsMatch(t, []model.ResourceType{model.TypeRdsCluster, model.TypeEcsService}, group.Types)

	require.NoError(t, runq(args("schedule", "--group", "dev", "-start", "0 9 * * MON-FRI", "-stop", "0 20 * * MON-FRI", "-timezone", "Asia/Tokyo")))
	group, err = s.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, group)
	assert.Equal(t, model.ModeSchedule, group.Mode)
	assert.Equal(t, "0 9 * * MON-FRI", group.StartCron)

	// cron のフィールドが残っている状態の unpin は、それらを消さず mode=schedule へ戻さなければならない
	require.NoError(t, runq(args("pin", "--group", "dev", "stopped")))
	require.NoError(t, runq(args("unpin", "--group", "dev")))
	group, err = s.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, group)
	assert.Equal(t, model.ModeSchedule, group.Mode, "unpin must resume schedule when crons are present")
	assert.Equal(t, "0 9 * * MON-FRI", group.StartCron, "unpin must not lose cron fields")

	require.NoError(t, runq(args("disable", "--group", "dev")))
	group, err = s.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, group)
	assert.Equal(t, model.ModeDisabled, group.Mode)
	assert.Equal(t, "0 9 * * MON-FRI", group.StartCron, "disable must keep other fields")

	require.NoError(t, runq(args("remove", "--group", "dev")))
	group, err = s.GetGroup(ctx, "dev")
	require.NoError(t, err)
	assert.Nil(t, group, "remove must delete the group")
}

func TestOverrideLifecycle(t *testing.T) {
	s, table := setup(t)
	ctx := context.Background()
	args := func(a ...string) []string { return append([]string{"-table", table}, a...) }

	// 未登録のグループへの override は拒否しなければならない
	err := runq(args("override", "--group", "ghost", "running", "-for", "2h"))
	require.Error(t, err, "want error for override without a group")

	require.NoError(t, runq(args("set-selector", "--group", "dev", "--tag-key", "env", "--tag-value", "dev", "--types", "rds-instance")))
	require.NoError(t, runq(args("pin", "--group", "dev", "stopped")))
	require.NoError(t, runq(args("override", "--group", "dev", "running", "-for", "2h")))

	o, err := s.GetOverride(ctx, "dev", time.Now())
	require.NoError(t, err)
	require.NotNil(t, o)
	assert.Equal(t, model.DesiredRunning, o.Desired)
	remaining := time.Until(time.Unix(o.ExpiresAt, 0))
	assert.InDelta(t, 2*time.Hour, remaining, float64(10*time.Minute), "expires_at not ~2h out")

	require.NoError(t, runq(args("clear-override", "--group", "dev")))
	o, err = s.GetOverride(ctx, "dev", time.Now())
	require.NoError(t, err)
	assert.Nil(t, o, "override after clear")
}

// 読み取り系の 3 コマンドは、その出力しか副作用を持たない
// そのため他のテストは cmdList / cmdShow / cmdDoctor を直接呼んでおり、Run の switch を通っていない
// つまり case のラベルを打ち間違えても、あるいは繋ぎ先を取り違えても、どのテストにも気づかれない
// ここだけが実際のコマンド名からハンドラまでの結線を通す
func TestReadOnlyCommandsDispatchFromCommandName(t *testing.T) {
	_, table := setup(t)
	args := func(a ...string) []string { return append([]string{"-table", table}, a...) }
	require.NoError(t, runq(args("set-selector", "--group", "dev", "--tag-key", "env", "--tag-value", "dev", "--types", "rds-instance")))

	cases := map[string][]string{
		"list":   args("list"),
		"show":   args("show", "--group", "dev"),
		"doctor": args("doctor"),
	}
	for want, c := range cases {
		t.Run(want, func(t *testing.T) {
			var buf bytes.Buffer

			require.NoError(t, Run(c, &buf))

			var got struct {
				Command string `json:"command"`
			}
			require.NoError(t, json.Unmarshal(buf.Bytes(), &got), buf.String())
			assert.Equal(t, want, got.Command, "コマンド名が別のハンドラへ繋がっている")
		})
	}
}

func TestValidationErrors(t *testing.T) {
	_, table := setup(t)
	args := func(a ...string) []string { return append([]string{"-table", table}, a...) }

	cases := map[string][]string{
		"missing tag value":          args("set-selector", "--group", "dev", "--tag-key", "env", "--types", "rds-instance"),
		"unknown selector type":      args("set-selector", "--group", "dev", "--tag-key", "env", "--tag-value", "dev", "--types", "sqs-queue"),
		"invalid group name":         args("set-selector", "--group", "-bad", "--tag-key", "env", "--tag-value", "dev", "--types", "rds-instance"),
		"bad desired":                args("pin", "--group", "dev", "on"),
		"no crons":                   args("schedule", "--group", "dev"),
		"invalid cron":               args("schedule", "--group", "dev", "-start", "not a cron"),
		"invalid timezone":           args("schedule", "--group", "dev", "-start", "0 9 * * *", "-timezone", "Not/AZone"),
		"disable unregistered group": args("disable", "--group", "unregistered"),
		"unpin unregistered group":   args("unpin", "--group", "unregistered"),
	}
	for desc, c := range cases {
		assert.Errorf(t, runq(c), "want error for: %s", desc)
	}
}
