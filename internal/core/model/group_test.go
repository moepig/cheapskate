package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidGroupName(t *testing.T) {
	// 長さの境界は上下の両方を検査する
	// 上限のみを検査した場合、正規表現の {0,63} が上限なしへ変化しても検出できない
	longest := "a" + strings.Repeat("b", 63)
	require.Len(t, longest, 64, "groupNameRE が許す最長")

	for _, name := range []string{"dev", "dev-1", "dev_1", "dev.1", "A", longest} {
		assert.NoErrorf(t, ValidGroupName(name), "%q should be valid", name)
	}
	for _, name := range []string{"", "-dev", ".dev", "dev#1", "dev/1", "dev 1", "日本語", longest + "b"} {
		assert.Errorf(t, ValidGroupName(name), "%q should be invalid", name)
	}
}

func TestParseGroupValid(t *testing.T) {
	g, err := ParseGroup(GroupSpec{
		Name: "dev", Mode: ModePinned, Desired: DesiredStopped,
		TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeRdsInstance},
	})
	require.NoError(t, err)
	assert.Equal(t, "dev", g.Name)
	assert.Equal(t, ModePinned, g.Mode)
	assert.Equal(t, DesiredStopped, g.Desired)
	assert.Equal(t, "env", g.Selector.TagKey)
}

func TestParseGroupDefaultsToDisabled(t *testing.T) {
	g, err := ParseGroup(GroupSpec{Name: "dev"})
	require.NoError(t, err)
	assert.Equal(t, ModeDisabled, g.Mode)
}

func TestParseGroupScheduleFields(t *testing.T) {
	g, err := ParseGroup(GroupSpec{
		Name: "dev", Mode: ModeSchedule, StartCron: "0 9 * * 1-5", StopCron: "0 21 * * 1-5", Timezone: "Asia/Tokyo",
		TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeEcsService},
	})
	require.NoError(t, err)
	assert.Equal(t, "0 9 * * 1-5", g.StartCron)
	assert.Equal(t, "0 21 * * 1-5", g.StopCron)
	assert.Equal(t, "Asia/Tokyo", g.Timezone)
}

// mode=schedule の場合、cron と timezone も ParseGroup が検証する
// この検証により、doctor は reconciler が従えない設定を config-error として報告できる (doctor.inspectGroup を参照)
func TestParseGroupValidatesScheduleFields(t *testing.T) {
	valid := GroupSpec{Name: "dev", Mode: ModeSchedule, TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeRdsInstance}}

	withSchedule := func(start, stop, tz string) GroupSpec {
		g := valid
		g.StartCron, g.StopCron, g.Timezone = start, stop, tz
		return g
	}

	_, err := ParseGroup(withSchedule("0 9 * * 1-5", "", ""))
	assert.NoError(t, err, "start cron alone is enough")
	_, err = ParseGroup(withSchedule("", "0 21 * * 1-5", "Asia/Tokyo"))
	assert.NoError(t, err, "stop cron alone is enough")

	_, err = ParseGroup(withSchedule("", "", "Asia/Tokyo"))
	assert.Error(t, err, "mode=schedule with no cron at all must be rejected")
	_, err = ParseGroup(withSchedule("every morning", "", ""))
	assert.Error(t, err, "an unparsable start cron must be rejected")
	_, err = ParseGroup(withSchedule("0 9 * * 1-5", "nightly", ""))
	assert.Error(t, err, "an unparsable stop cron must be rejected")
	_, err = ParseGroup(withSchedule("0 9 * * 1-5", "", "Mars/Olympus"))
	assert.Error(t, err, "an unknown timezone must be rejected")

	// 空の timezone は不正ではなく、reconciler の既定タイムゾーンを用いることを表す
	_, err = ParseGroup(withSchedule("0 9 * * 1-5", "0 21 * * 1-5", ""))
	assert.NoError(t, err, "an empty timezone falls back to the reconciler default")

	// cron が不正な場合も、mode が cron を参照しないときは設定エラーとしない
	// 参照しない設定を理由に失敗させた場合、壊れたスケジュールを pin で回避する経路が失われる
	pinned := withSchedule("every morning", "", "")
	pinned.Mode, pinned.Desired = ModePinned, DesiredStopped
	_, err = ParseGroup(pinned)
	assert.NoError(t, err, "mode=pinned does not follow the crons, so it must not be judged by them")
}

func TestParseGroupSelectorRules(t *testing.T) {
	// セレクタを持たない disabled は正常であり、作成直後の未設定グループが該当する
	g, err := ParseGroup(GroupSpec{Name: "dev", Mode: ModeDisabled})
	require.NoError(t, err)
	assert.True(t, g.Selector.Empty())

	// セレクタなしでの有効化 (pinned/schedule) は設定エラーとする
	_, err = ParseGroup(GroupSpec{Name: "dev", Mode: ModePinned, Desired: DesiredStopped})
	assert.Error(t, err)
	_, err = ParseGroup(GroupSpec{Name: "dev", Mode: ModeSchedule, StartCron: "0 9 * * 1-5"})
	assert.Error(t, err)

	// 壊れたセレクタは disabled の場合も拒否する
	// 設定済みのセレクタは妥当でなければならないためである
	_, err = ParseGroup(GroupSpec{Name: "dev", Mode: ModeDisabled, TagValue: "dev", Types: []ResourceType{TypeRdsInstance}})
	assert.Error(t, err)

	// 有効なグループでも同じく拒否する
	// 空ではないが妥当でないセレクタは Discover へ渡せず、reconciler はメンバーを列挙できない
	// セレクタなしでの有効化とは別の経路であるため、mode ごとに検査する
	for _, mode := range []Mode{ModePinned, ModeSchedule} {
		_, err = ParseGroup(GroupSpec{
			Name: "dev", Mode: mode, Desired: DesiredStopped, StartCron: "0 9 * * 1-5",
			TagKey: "env", TagValue: "dev", Types: []ResourceType{"sqs-queue"}, // 未知のリソース種別
		})
		assert.Errorf(t, err, "mode=%s must reject a non-empty but invalid selector", mode)
	}
}

func TestParseGroupRejects(t *testing.T) {
	valid := GroupSpec{TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeRdsInstance}}
	cases := []GroupSpec{
		{Name: "-bad", Mode: ModeDisabled}, // 不正なグループ名
		{Name: "dev", Mode: "sometimes", TagKey: valid.TagKey, TagValue: valid.TagValue, Types: valid.Types},               // 未知の mode
		{Name: "dev", Mode: ModePinned, TagKey: valid.TagKey, TagValue: valid.TagValue, Types: valid.Types},                // desired なしの pinned
		{Name: "dev", Mode: ModePinned, Desired: "on", TagKey: valid.TagKey, TagValue: valid.TagValue, Types: valid.Types}, // 不正な desired
	}
	for _, item := range cases {
		_, err := ParseGroup(item)
		assert.Errorf(t, err, "want error for %+v", item)
	}
}

// pin は cron を削除しない
// mode=pinned では cron が作用しないため、unpin と schedule による復帰のために保持する
func TestPinPreservesCronsAndSetsDesired(t *testing.T) {
	base := GroupSpec{
		Name: "dev", Mode: ModeSchedule, StartCron: "0 9 * * 1-5", StopCron: "0 21 * * 1-5", Timezone: "Asia/Tokyo",
		TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeRdsInstance},
	}
	got, err := base.Pin(DesiredStopped)
	require.NoError(t, err)
	assert.Equal(t, ModePinned, got.Mode)
	assert.Equal(t, DesiredStopped, got.Desired)
	assert.Equal(t, "0 9 * * 1-5", got.StartCron)
	assert.Equal(t, "0 21 * * 1-5", got.StopCron)
	assert.Equal(t, "Asia/Tokyo", got.Timezone)

	_, err = base.Pin("paused")
	assert.Error(t, err, "pin must reject a desired state that is neither running nor stopped")
}

func TestUnpinPicksScheduleOrDisabled(t *testing.T) {
	withSelector := GroupSpec{
		Name: "dev", Mode: ModePinned, Desired: DesiredStopped,
		TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeRdsInstance},
	}

	scheduled := withSelector
	scheduled.StartCron = "0 9 * * 1-5"
	got, err := scheduled.Unpin()
	require.NoError(t, err)
	assert.Equal(t, ModeSchedule, got.Mode, "a group with crons resumes its schedule")

	got, err = withSelector.Unpin()
	require.NoError(t, err)
	assert.Equal(t, ModeDisabled, got.Mode, "a group that was never scheduled has nowhere to resume to")
}

// schedule は desired を保持しない
// mode=schedule では cron が desired state を決定し、pin の設定値は参照されないためである
func TestWithScheduleDropsPinnedDesired(t *testing.T) {
	pinned := GroupSpec{
		Name: "dev", Mode: ModePinned, Desired: DesiredStopped,
		TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeRdsInstance},
	}
	got, err := pinned.WithSchedule(ScheduleSpec{StartCron: "0 9 * * 1-5", StopCron: "0 21 * * 1-5"})
	require.NoError(t, err)
	assert.Equal(t, ModeSchedule, got.Mode)
	assert.Equal(t, DesiredNone, got.Desired, "the stale pinned desired must not survive")
}

// disabled は、結果を検証しない唯一の遷移である
// 設定が壊れているグループの管理を停止する唯一の手段であり、その設定の破損を理由に失敗してはならない
func TestDisabledAlwaysSucceedsEvenForUnusableConfig(t *testing.T) {
	broken := GroupSpec{Name: "dev", Mode: ModeSchedule, StartCron: "every morning", TagKey: "aws:reserved", TagValue: "x"}
	_, err := ParseGroup(broken)
	require.Error(t, err, "この設定は ParseGroup が拒否する (前提の確認)")

	got := broken.Disabled()
	assert.Equal(t, ModeDisabled, got.Mode)
	assert.Equal(t, "every morning", got.StartCron, "他の設定はそのまま残す")
}

func TestWithSelectorPreservesModeAndNormalizesTypes(t *testing.T) {
	pinned := GroupSpec{
		Name: "dev", Mode: ModePinned, Desired: DesiredStopped,
		TagKey: "env", TagValue: "old", Types: []ResourceType{TypeRdsInstance},
	}
	got, err := pinned.WithSelector(Selector{
		TagKey: "env", TagValue: "new", Types: []ResourceType{TypeRdsCluster, TypeEc2Instance, TypeRdsCluster},
	})
	require.NoError(t, err)
	assert.Equal(t, ModePinned, got.Mode, "changing the selector must not change the mode")
	assert.Equal(t, DesiredStopped, got.Desired)
	assert.Equal(t, "new", got.TagValue)
	assert.Equal(t, []ResourceType{TypeEc2Instance, TypeRdsCluster}, got.Types, "deduplicated and sorted")

	_, err = pinned.WithSelector(Selector{TagKey: "env"})
	assert.Error(t, err, "an invalid selector must be rejected")
}

// いずれの遷移も結果を ParseGroup へ通す
// これにより、アプリケーション層は保存可能かつ reconciler が従えない設定を書き込めない
func TestTransitionsRejectResultsParseGroupWouldReject(t *testing.T) {
	noSelector := GroupSpec{Name: "dev", Mode: ModeDisabled}

	_, err := noSelector.Pin(DesiredStopped)
	assert.Error(t, err, "mode=pinned without a selector is a config error, so pin must refuse it")

	_, err = noSelector.WithSchedule(ScheduleSpec{StartCron: "0 9 * * 1-5"})
	assert.Error(t, err, "mode=schedule without a selector is a config error, so schedule must refuse it")

	// 保存済みの cron が不正な状態での schedule への復帰も拒否する
	// 操作は成功として報告される一方、reconciler が毎サイクル同じ設定エラーを出力するためである
	brokenCron := GroupSpec{
		Name: "dev", Mode: ModePinned, Desired: DesiredStopped, StartCron: "every morning",
		TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeRdsInstance},
	}
	_, err = brokenCron.Unpin()
	assert.Error(t, err, "unpin must not silently move a group onto crons the reconciler cannot follow")
}
