package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/app/doctor"
	"cheapskate/internal/app/groups"
	"cheapskate/internal/app/port"
	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	mocks "cheapskate/internal/state/mocks"
)

// フラグの誤用は flag.ExitOnError による os.Exit ではなく、Run() のエラー（ContinueOnError）として返さなければならない
// os.Exit ではテストバイナリや、このコードを組み込んだプロセスまで落ちてしまう
func TestFlagMisuseReturnsErrorInsteadOfExiting(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown global flag", []string{"-bogus"}, "flag provided but not defined"},
		{"unknown schedule flag", []string{"-table", "t", "schedule", "--group", "dev", "-bogus"}, "flag provided but not defined"},
		{"unknown override flag", []string{"-table", "t", "override", "--group", "dev", "running", "-for", "2h", "-bogus"}, "flag provided but not defined"},
		{"malformed duration", []string{"-table", "t", "override", "--group", "dev", "running", "-for", "not-a-duration"}, "invalid value"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			err := Run(c.args, &out)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
			assert.Empty(t, out.String(), "a failing command must leave nothing on stdout — main prints the error as JSON on stderr")
		})
	}
}

// -h を通常の（JSON の）エラーとして報告してはならない
// main は flag.ErrHelp に Usage テキストと終了コード 0 で応じる
func TestHelpReturnsFlagErrHelp(t *testing.T) {
	assert.ErrorIs(t, Run([]string{"-h"}, io.Discard), flag.ErrHelp)
}

// コマンド名の打ち間違いをフラグのエラーと取り違えず、-h を案内しなければならない
func TestUnknownAndMissingCommand(t *testing.T) {
	err := Run([]string{"-table", "t", "bogus"}, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown command "bogus"`)

	err = Run(nil, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing command")
}

// 引数の不備は AWS へ 1 度も触れる前に、そのコマンド自身の言葉で報告しなければならない
// --group を落とした呼び出しが「グループ "" が見つからない」になると、打ち間違いなのかフラグの付け忘れなのかを利用者が切り分けられない
func TestCommandArgumentValidation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"show without group", []string{"show"}, "--group is required"},
		{"set-selector without group", []string{"set-selector", "--tag-key", "env", "--tag-value", "dev", "--types", "rds-instance"}, "--group is required"},
		{"remove without group", []string{"remove"}, "--group is required"},
		{"pin without group", []string{"pin", "stopped"}, "--group is required"},
		{"unpin without group", []string{"unpin"}, "--group is required"},
		{"schedule without group", []string{"schedule", "-start", "0 9 * * *"}, "--group is required"},
		{"disable without group", []string{"disable"}, "--group is required"},
		{"override without group", []string{"override", "running", "-for", "2h"}, "--group is required"},
		{"clear-override without group", []string{"clear-override"}, "--group is required"},

		// 位置引数は running|stopped のどちらか 1 つでなければならない
		// 落としたぶんを既定値で補うと、意図しない向きへ倒すことになる
		{"pin without a desired state", []string{"pin", "--group", "dev"}, "exactly one"},
		{"pin with two desired states", []string{"pin", "--group", "dev", "running", "stopped"}, "exactly one"},
		{"override without a desired state", []string{"override", "--group", "dev", "-for", "2h"}, "exactly one"},
		{"override with two desired states", []string{"override", "--group", "dev", "running", "stopped", "-for", "2h"}, "exactly one"},

		// override に期限は必須である
		// -for を落としたぶんを既定の期間で補うと、利用者が意図していない時刻に勝手に失効する
		{"override without a duration", []string{"override", "--group", "dev", "running"}, "-for"},
		{"override with a zero duration", []string{"override", "--group", "dev", "running", "-for", "0s"}, "-for"},
		{"override with a negative duration", []string{"override", "--group", "dev", "running", "-for", "-1h"}, "-for"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			err := Run(append([]string{"-table", "t"}, c.args...), &out)
			require.Error(t, err)
			assert.Contains(t, err.Error(), c.want)
			assert.Empty(t, out.String(), "検証に失敗したコマンドは stdout に何も残してはならない")
		})
	}
}

// state テーブル名はどのコマンドの前提でもあり、これがなければ何も読めない
// 決め打ちの既定値へ落ちると、意図しないテーブルを読み書きしうる
func TestMissingStateTableIsReported(t *testing.T) {
	t.Setenv("CHEAPSKATE_TABLE", "")

	err := Run([]string{"list"}, io.Discard)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "state table not set")
}

func newTestStore(t *testing.T) (*mocks.DynaStore, *state.Store) {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	return db, state.New(api, "t")
}

// cmdList の JSON が "list" コマンドの振る舞いのすべてである
// ここでの退行（フィールドの欠落や改名）は、これがなければどのテストにも気づかれない
func TestCmdListRendersGroupsAsJSON(t *testing.T) {
	_, s := newTestStore(t)
	ctx := context.Background()

	_, err := groups.SetSelector(ctx, s, "dev", model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}})
	require.NoError(t, err)
	require.NoError(t, groups.Pin(ctx, s, "dev", model.DesiredStopped))

	_, err = groups.SetSelector(ctx, s, "staging", model.Selector{TagKey: "env", TagValue: "staging", Types: []model.ResourceType{model.TypeEcsService}})
	require.NoError(t, err)
	_, err = groups.Schedule(ctx, s, "staging", model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 20 * * *", Timezone: "Asia/Tokyo"})
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, cmdList(ctx, s, &buf))

	var got listOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "list", got.Command)
	groups := byName(t, got.Groups)
	require.Contains(t, groups, "dev")
	require.Contains(t, groups, "staging")

	dev := groups["dev"]
	assert.Equal(t, model.ModePinned, dev.Mode)
	assert.Equal(t, model.DesiredStopped, dev.Desired, "a pinned group must report its desired state")
	require.NotNil(t, dev.Selector)
	assert.Equal(t, selectorJSON{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}}, *dev.Selector)
	assert.Nil(t, dev.Override)
	assert.Empty(t, dev.Error)

	staging := groups["staging"]
	assert.Equal(t, model.ModeSchedule, staging.Mode)
	assert.Equal(t, "0 9 * * *", staging.StartCron)
	assert.Equal(t, "0 20 * * *", staging.StopCron)
	assert.Equal(t, "Asia/Tokyo", staging.Timezone)
}

// テーブルが空でも null ではなく JSON 配列を出力しなければならない
// 呼び出し側が無条件に jq へ流せるようにするためである
func TestCmdListWithNoGroupsEmitsEmptyArray(t *testing.T) {
	_, s := newTestStore(t)
	var buf bytes.Buffer
	require.NoError(t, cmdList(context.Background(), s, &buf))
	assert.JSONEq(t, `{"command":"list","groups":[]}`, buf.String())
}

func byName(t *testing.T, groups []groupJSON) map[string]groupJSON {
	t.Helper()
	m := make(map[string]groupJSON, len(groups))
	for _, g := range groups {
		m[g.Name] = g
	}
	return m
}

// 壊れた行（override の破損など）は、そのグループの "error" フィールドとして現れなければならない
// 一覧の残りを中断させてはならない
func TestCmdListRendersPerRowErrorWithoutAbortingOthers(t *testing.T) {
	f, s := newTestStore(t)
	ctx := context.Background()

	_, err := groups.SetSelector(ctx, s, "broken", model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}})
	require.NoError(t, err)
	require.NoError(t, groups.Pin(ctx, s, "broken", model.DesiredStopped))
	f.Seed(map[string]types.AttributeValue{
		"pk":         &types.AttributeValueMemberS{Value: "override#broken"},
		"desired":    &types.AttributeValueMemberS{Value: "not-a-valid-state"},
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	_, err = groups.SetSelector(ctx, s, "fine", model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}})
	require.NoError(t, err)
	require.NoError(t, groups.Pin(ctx, s, "fine", model.DesiredStopped))

	var buf bytes.Buffer
	require.NoError(t, cmdList(ctx, s, &buf))

	var got listOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	groups := byName(t, got.Groups)
	require.Contains(t, groups, "broken")
	require.Contains(t, groups, "fine")
	assert.Contains(t, groups["broken"].Error, "broken: override desired must be running|stopped")
	assert.Equal(t, model.ModePinned, groups["fine"].Mode)
	assert.Empty(t, groups["fine"].Error, "an unrelated group must not be affected by another group's error")
}

func TestCmdListPropagatesScanError(t *testing.T) {
	f, s := newTestStore(t)
	f.FailOn("scan", "", assert.AnError)

	var buf bytes.Buffer
	assert.Error(t, cmdList(context.Background(), s, &buf))
}

// cmdShow の JSON の形（showOutput と showResource）は、他のどこでもまったくテストされていない
// CLI の他のコマンドは store への副作用で検証できるが、"show" は出力しかしないためである
func TestCmdShowRendersResourcesConfigAndLiveState(t *testing.T) {
	_, s := newTestStore(t)
	ctx := context.Background()

	sel := model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeEcsService}}
	_, err := groups.SetSelector(ctx, s, "dev", sel)
	require.NoError(t, err)
	require.NoError(t, groups.Pin(ctx, s, "dev", model.DesiredRunning))

	resource := model.Resource{
		Type: model.TypeEcsService,
		Ref:  "dev-cluster/api",
		ARN:  "arn:aws:ecs:ap-northeast-1:123456789012:service/dev-cluster/api",
		Tags: map[string]string{
			model.EcsDesiredCountTagKey: "2",
			model.EcsScalingMinTagKey:   "1",
			model.EcsScalingMaxTagKey:   "3",
		},
	}
	d := &porttest.Discoverer{Resources: []model.Resource{resource}}

	describers := map[model.ResourceType]port.Describer{
		model.TypeEcsService: porttest.Describer{Obs: model.Observation{State: model.StateRunning, Detail: "desiredCount=2"}},
	}

	var buf bytes.Buffer
	require.NoError(t, cmdShow(ctx, s, d, describers, []string{"--group", "dev"}, &buf))

	var got showOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "show", got.Command)
	assert.Equal(t, "dev", got.Group.Name)
	assert.Equal(t, model.ModePinned, got.Group.Mode)
	require.Len(t, got.Resources, 1)
	assert.Equal(t, "dev-cluster/api", got.Resources[0].Ref)
	require.NotNil(t, got.Resources[0].Live)
	assert.Equal(t, model.StateRunning, got.Resources[0].Live.State)
	require.NotNil(t, got.Resources[0].Config)
	assert.Equal(t, map[string]any{"desired_count": "2", "min": "1", "max": "3"}, got.Resources[0].Config)
	assert.Equal(t, []model.Selector{sel}, d.Selectors, "show must discover with the group's own selector")
}

// 探索の失敗は show 全体の失敗ではなく discover_error として載せる
// 権限不足やスロットリングで、設定・override・ステータスまで一緒に見えなくなってはならない
// これらは探索に一切依存せず、しかも障害時にこそ必要な情報である
func TestCmdShowRendersDiscoverErrorWithoutFailing(t *testing.T) {
	_, s := newTestStore(t)
	ctx := context.Background()

	sel := model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}}
	_, err := groups.SetSelector(ctx, s, "dev", sel)
	require.NoError(t, err)
	require.NoError(t, groups.Pin(ctx, s, "dev", model.DesiredStopped))

	var buf bytes.Buffer
	require.NoError(t, cmdShow(ctx, s, &porttest.Discoverer{Err: assert.AnError}, nil, []string{"--group", "dev"}, &buf))

	var got showOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Contains(t, got.DiscoverErr, assert.AnError.Error())
	assert.Empty(t, got.Resources, "メンバーが分からない以上、一部だけを載せてはならない")
	assert.Equal(t, model.ModePinned, got.Group.Mode, "探索が落ちても設定は読めているので見せる")
}

// Describe の失敗はリソース単位の live_error に留め、その行や他の行を消してはならない
// 1 リソースが読めないことと、グループの構成が分からないことは別である
func TestCmdShowRendersPerResourceLiveError(t *testing.T) {
	_, s := newTestStore(t)
	ctx := context.Background()

	sel := model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}}
	_, err := groups.SetSelector(ctx, s, "dev", sel)
	require.NoError(t, err)
	require.NoError(t, groups.Pin(ctx, s, "dev", model.DesiredStopped))
	d := &porttest.Discoverer{Resources: []model.Resource{{Type: model.TypeRdsInstance, Ref: "dev-db"}}}
	describers := map[model.ResourceType]port.Describer{model.TypeRdsInstance: porttest.Describer{Err: assert.AnError}}

	var buf bytes.Buffer
	require.NoError(t, cmdShow(ctx, s, d, describers, []string{"--group", "dev"}, &buf))

	var got showOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got.Resources, 1, "live state が読めなくても行そのものは残る")
	assert.Equal(t, "dev-db", got.Resources[0].Ref)
	assert.Nil(t, got.Resources[0].Live)
	assert.Contains(t, got.Resources[0].LiveErr, assert.AnError.Error())
	assert.Empty(t, got.DiscoverErr, "探索は成功しているので discover_error は空でなければならない")
}

// 壊れた行はグループの "error" フィールドとして現れ、show 全体を落としてはならない
// cmdList については TestCmdListRendersPerRowErrorWithoutAbortingOthers が同じ約束を担保している
func TestCmdShowRendersPerRowError(t *testing.T) {
	f, s := newTestStore(t)
	ctx := context.Background()

	sel := model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}}
	_, err := groups.SetSelector(ctx, s, "dev", sel)
	require.NoError(t, err)
	require.NoError(t, groups.Pin(ctx, s, "dev", model.DesiredStopped))
	f.Seed(map[string]types.AttributeValue{
		"pk":         &types.AttributeValueMemberS{Value: "override#dev"},
		"desired":    &types.AttributeValueMemberS{Value: "not-a-valid-state"},
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})

	var buf bytes.Buffer
	require.NoError(t, cmdShow(ctx, s, porttest.NewDiscoverer(), nil, []string{"--group", "dev"}, &buf))

	var got showOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Contains(t, got.Group.Error, "override desired must be running|stopped")
	assert.Equal(t, "dev", got.Group.Name)
}

func TestCmdShowRequiresGroupFlag(t *testing.T) {
	_, s := newTestStore(t)
	assert.Error(t, cmdShow(context.Background(), s, nil, nil, nil, &bytes.Buffer{}))
}

func TestCmdShowRejectsUnknownGroup(t *testing.T) {
	_, s := newTestStore(t)
	err := cmdShow(context.Background(), s, nil, nil, []string{"--group", "ghost"}, &bytes.Buffer{})
	assert.Error(t, err)
}

func TestNewConfigJSON(t *testing.T) {
	assert.Nil(t, newConfigJSON(model.GroupSpec{}).Selector, "a group with no selector must omit the field, not print an empty object")

	item := model.GroupSpec{
		Mode: model.ModeSchedule, StartCron: "0 9 * * *", Timezone: "Asia/Tokyo",
		TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance},
	}
	cfg := newConfigJSON(item)
	assert.Equal(t, model.ModeSchedule, cfg.Mode)
	assert.Equal(t, "0 9 * * *", cfg.StartCron)
	require.NotNil(t, cfg.Selector)
	assert.Equal(t, selectorJSON{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}}, *cfg.Selector)
}

// 保存された override は epoch 秒を持つ
// 出力は RFC3339 の UTC でなければならず、CLI がどこで動いても曖昧さがないようにする
func TestNewOverrideJSON(t *testing.T) {
	assert.Nil(t, newOverrideJSON(nil))
	expiresAt := time.Date(2026, 7, 20, 15, 0, 0, 0, time.UTC)
	o := &model.Override{Desired: model.DesiredRunning, ExpiresAt: expiresAt.Unix()}
	assert.Equal(t, &overrideJSON{Desired: model.DesiredRunning, ExpiresAt: "2026-07-20T15:00:00Z"}, newOverrideJSON(o))
}

// 変更系のコマンドはどれも、自身の名前と変更したグループを含む JSON オブジェクトを 1 つ出力する
// jq へ流す呼び出し側は、どのコマンドが動いたかに関わらずそれに依拠できなければならない
func TestMutatingCommandsPrintJSON(t *testing.T) {
	_, s := newTestStore(t)
	ctx := context.Background()

	capture := func(t *testing.T, fn func(io.Writer) error) mutationResult {
		t.Helper()
		var buf bytes.Buffer
		require.NoError(t, fn(&buf))
		var got mutationResult
		require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
		return got
	}

	sel := []string{"--group", "dev", "--tag-key", "env", "--tag-value", "dev", "--types", string(model.TypeRdsInstance)}
	got := capture(t, func(w io.Writer) error { return cmdSetSelector(ctx, s, sel, w) })
	assert.Equal(t, "set-selector", got.Command)
	assert.Equal(t, "dev", got.Group)
	assert.True(t, got.Created, "the group did not exist yet")
	assert.Equal(t, model.ModeDisabled, got.Mode)
	require.NotNil(t, got.Selector)
	assert.Equal(t, selectorJSON{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}}, *got.Selector)

	got = capture(t, func(w io.Writer) error { return cmdSetSelector(ctx, s, sel, w) })
	assert.False(t, got.Created, "the group already exists")

	got = capture(t, func(w io.Writer) error {
		return cmdPin(ctx, s, []string{"--group", "dev", string(model.DesiredStopped)}, w)
	})
	assert.Equal(t, "pin", got.Command)
	assert.Equal(t, model.ModePinned, got.Mode)
	assert.Equal(t, model.DesiredStopped, got.Desired)

	got = capture(t, func(w io.Writer) error {
		return cmdSchedule(ctx, s, []string{"--group", "dev", "-start", "0 9 * * *", "-timezone", "Asia/Tokyo"}, w)
	})
	assert.Equal(t, "schedule", got.Command)
	assert.Equal(t, model.ModeSchedule, got.Mode)
	assert.Equal(t, "0 9 * * *", got.StartCron)
	assert.Equal(t, "Asia/Tokyo", got.Timezone)

	got = capture(t, func(w io.Writer) error {
		return cmdPin(ctx, s, []string{"--group", "dev", string(model.DesiredRunning)}, w)
	})
	require.Equal(t, model.ModePinned, got.Mode)
	got = capture(t, func(w io.Writer) error { return cmdUnpin(ctx, s, []string{"--group", "dev"}, w) })
	assert.Equal(t, "unpin", got.Command)
	assert.Equal(t, model.ModeSchedule, got.Mode, "unpin resumes the schedule it reports")

	got = capture(t, func(w io.Writer) error {
		return cmdOverride(ctx, s, []string{"--group", "dev", string(model.DesiredRunning), "-for", "2h"}, w)
	})
	assert.Equal(t, "override", got.Command)
	require.NotNil(t, got.Override)
	assert.Equal(t, model.DesiredRunning, got.Override.Desired)
	expiresAt, err := time.Parse(time.RFC3339, got.Override.ExpiresAt)
	require.NoError(t, err, "expires_at must be RFC3339")
	assert.WithinDuration(t, time.Now().Add(2*time.Hour), expiresAt, time.Minute)

	got = capture(t, func(w io.Writer) error { return cmdClearOverride(ctx, s, []string{"--group", "dev"}, w) })
	assert.Equal(t, "clear-override", got.Command)
	assert.Nil(t, got.Override)

	got = capture(t, func(w io.Writer) error { return cmdDisable(ctx, s, []string{"--group", "dev"}, w) })
	assert.Equal(t, "disable", got.Command)
	assert.Equal(t, model.ModeDisabled, got.Mode)

	got = capture(t, func(w io.Writer) error { return cmdRemove(ctx, s, []string{"--group", "dev"}, w) })
	assert.Equal(t, "remove", got.Command)
	assert.Equal(t, "dev", got.Group)
}

func TestResourceConfig(t *testing.T) {
	assert.Nil(t, resourceConfig(model.Resource{Type: model.TypeRdsInstance}), "non-ecs-service types carry no tag-derived config")
	assert.Nil(t, resourceConfig(model.Resource{Type: model.TypeEcsService}), "no scaling tags set")

	r := model.Resource{Type: model.TypeEcsService, Tags: map[string]string{model.EcsDesiredCountTagKey: "2"}}
	assert.Equal(t, map[string]string{"desired_count": "2"}, resourceConfig(r), "未設定のタグは出さない")
}

// 検出項目がなくても null ではなく JSON 配列を出力しなければならない（cmdList と同じ約束）
// pruned も --prune の有無にかかわらず必ず出し、「消した/消していない」を出力そのものから読めるようにする
func TestCmdDoctorWithCleanTableEmitsEmptyArray(t *testing.T) {
	_, s := newTestStore(t)
	var buf bytes.Buffer
	require.NoError(t, cmdDoctor(context.Background(), s, porttest.NewDiscoverer(), nil, &buf))
	assert.JSONEq(t, `{"command":"doctor","findings":[],"pruned":0}`, buf.String())
}

func TestCmdDoctorReportsAndPrunesOrphans(t *testing.T) {
	db, s := newTestStore(t)
	ctx := context.Background()
	db.Seed(map[string]types.AttributeValue{
		"pk":         &types.AttributeValueMemberS{Value: "override#ghost"},
		"desired":    &types.AttributeValueMemberS{Value: string(model.DesiredRunning)},
		"expires_at": &types.AttributeValueMemberN{Value: "99999999999"},
	})

	var buf bytes.Buffer
	require.NoError(t, cmdDoctor(ctx, s, porttest.NewDiscoverer(), nil, &buf))

	var got doctorOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, "doctor", got.Command)
	require.Len(t, got.Findings, 1)
	assert.Equal(t, doctor.KindOrphanOverride, got.Findings[0].Kind)
	assert.Equal(t, "override#ghost", got.Findings[0].PK)
	assert.True(t, got.Findings[0].Prunable)
	assert.Equal(t, 1, got.Counts[doctor.KindOrphanOverride])
	assert.Zero(t, got.Pruned, "a read-only run must delete nothing")
	assert.NotNil(t, db.Item("override#ghost"))

	buf.Reset()
	require.NoError(t, cmdDoctor(ctx, s, porttest.NewDiscoverer(), []string{"--prune"}, &buf))

	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Equal(t, 1, got.Pruned)
	assert.Nil(t, db.Item("override#ghost"))
}

func TestCmdDoctorStuckAfterFlag(t *testing.T) {
	db, s := newTestStore(t)
	ctx := context.Background()
	_, err := groups.SetSelector(ctx, s, "dev", model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}})
	require.NoError(t, err)
	require.NoError(t, groups.Pin(ctx, s, "dev", model.DesiredStopped))
	db.Seed(map[string]types.AttributeValue{
		"pk":                  &types.AttributeValueMemberS{Value: "status#rds-instance#db1"},
		"transitioning_since": &types.AttributeValueMemberS{Value: time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
	})
	disc := porttest.NewDiscoverer()
	disc.ByTagValue["dev"] = []model.Resource{{Type: model.TypeRdsInstance, Ref: "db1"}}

	var buf bytes.Buffer
	require.NoError(t, cmdDoctor(ctx, s, disc, []string{"--stuck-after", "2h"}, &buf))
	var got doctorOutput
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	assert.Empty(t, got.Findings, "an hour of transitioning is under a 2h threshold")

	buf.Reset()
	require.NoError(t, cmdDoctor(ctx, s, disc, []string{"--stuck-after", "30m"}, &buf))
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got))
	require.Len(t, got.Findings, 1)
	assert.Equal(t, doctor.KindStuckTransitioning, got.Findings[0].Kind)
}
