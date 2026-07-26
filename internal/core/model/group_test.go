package model

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidGroupName(t *testing.T) {
	// 長さの境界は両側を見る
	// 上限ちょうどだけを試すと、正規表現の {0,63} が上限なしに緩んでも気づけない
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

// mode=schedule のときは cron と timezone も ParseGroup が検証する
// この検証がここにあることが、doctor が「reconciler が従えない設定」を config-error として報告できる根拠になっている（doctor.inspectGroup を参照）
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

	// 空の timezone は不正ではなく、reconciler の既定タイムゾーンを使うことを意味する
	_, err = ParseGroup(withSchedule("0 9 * * 1-5", "0 21 * * 1-5", ""))
	assert.NoError(t, err, "an empty timezone falls back to the reconciler default")

	// cron が不正でも、mode がそれを見ない側なら設定エラーではない
	// 見ないものを理由に失敗させると、壊れたスケジュールを pin で退避させる経路が塞がる
	pinned := withSchedule("every morning", "", "")
	pinned.Mode, pinned.Desired = ModePinned, DesiredStopped
	_, err = ParseGroup(pinned)
	assert.NoError(t, err, "mode=pinned does not follow the crons, so it must not be judged by them")
}

func TestParseGroupSelectorRules(t *testing.T) {
	// セレクタが一切ない disabled は正常で、作成直後の未設定グループにあたる
	g, err := ParseGroup(GroupSpec{Name: "dev", Mode: ModeDisabled})
	require.NoError(t, err)
	assert.True(t, g.Selector.Empty())

	// セレクタなしで有効化（pinned/schedule）するのは設定エラー
	_, err = ParseGroup(GroupSpec{Name: "dev", Mode: ModePinned, Desired: DesiredStopped})
	assert.Error(t, err)
	_, err = ParseGroup(GroupSpec{Name: "dev", Mode: ModeSchedule, StartCron: "0 9 * * 1-5"})
	assert.Error(t, err)

	// 壊れたセレクタは disabled でも拒否する
	// いったん設定したなら妥当でなければならない
	_, err = ParseGroup(GroupSpec{Name: "dev", Mode: ModeDisabled, TagValue: "dev", Types: []ResourceType{TypeRdsInstance}})
	assert.Error(t, err)

	// 有効なグループでも同じく拒否する
	// 空でないのに妥当でないセレクタは Discover に渡せず、reconciler がメンバーを数え上げられない
	// 「セレクタなしで有効化」とは別の経路なので、mode ごとに確かめる
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

// pin は cron を消さない
// mode=pinned では cron が作用しないので、あとで unpin や schedule で戻せるように残す
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

// schedule は desired を持ち越さない
// mode=schedule では cron が desired state を決めるので、古い pin の値が残ると読み手を惑わせる
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

// disabled は結果を検証しない唯一の遷移である
// 設定が壊れているグループの管理を今すぐ止めるための最後の手段であり、まさにその壊れた設定を理由に失敗してはならない
func TestDisabledAlwaysSucceedsEvenForUnusableConfig(t *testing.T) {
	broken := GroupSpec{Name: "dev", Mode: ModeSchedule, StartCron: "every morning", TagKey: "aws:reserved", TagValue: "x"}
	_, err := ParseGroup(broken)
	require.Error(t, err, "この設定は ParseGroup が拒否する（前提の確認）")

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

// 遷移はどれも結果を ParseGroup へ通す
// これがあることで、アプリケーション層は「保存はできたが reconciler は従えない」設定を書けない
func TestTransitionsRejectResultsParseGroupWouldReject(t *testing.T) {
	noSelector := GroupSpec{Name: "dev", Mode: ModeDisabled}

	_, err := noSelector.Pin(DesiredStopped)
	assert.Error(t, err, "mode=pinned without a selector is a config error, so pin must refuse it")

	_, err = noSelector.WithSchedule(ScheduleSpec{StartCron: "0 9 * * 1-5"})
	assert.Error(t, err, "mode=schedule without a selector is a config error, so schedule must refuse it")

	// 保存済みの cron が使えない状態で schedule へ戻すことも許さない
	// 成功したように見えて、reconciler が毎サイクル同じ設定エラーを出し続ける状態になるためである
	brokenCron := GroupSpec{
		Name: "dev", Mode: ModePinned, Desired: DesiredStopped, StartCron: "every morning",
		TagKey: "env", TagValue: "dev", Types: []ResourceType{TypeRdsInstance},
	}
	_, err = brokenCron.Unpin()
	assert.Error(t, err, "unpin must not silently move a group onto crons the reconciler cannot follow")
}
