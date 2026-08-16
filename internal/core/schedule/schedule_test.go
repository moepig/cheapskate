package schedule

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
)

const tokyo = "Asia/Tokyo"

// 指定した JST の壁時計時刻に対応する UTC の時刻を返す
func jst(t *testing.T, value string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(tokyo)
	require.NoError(t, err)
	ts, err := time.ParseInLocation("2006-01-02 15:04", value, loc)
	require.NoError(t, err)
	return ts.UTC()
}

func businessHours() model.GroupConfig {
	return model.GroupConfig{
		Name:      "dev",
		Mode:      model.ModeSchedule,
		StartCron: "0 9 * * MON-FRI",
		StopCron:  "0 20 * * MON-FRI",
		Timezone:  tokyo,
	}
}

func resolve(t *testing.T, group model.GroupConfig, o *model.Override, now time.Time) model.DesiredState {
	t.Helper()
	got, err := ResolveDesired(group, o, now, "UTC")
	require.NoError(t, err)
	return got
}

func TestDisabledReturnsEmpty(t *testing.T) {
	tag := model.GroupConfig{Name: "dev", Mode: model.ModeDisabled}
	assert.Empty(t, resolve(t, tag, nil, time.Now()), "disabled must resolve to empty")
}

// disabled は override より優先度の高い停止である
// disable は override# アイテムを削除しないため、pin → override → disable の順の操作により、disabled のグループに未失効の override が残る状態となる
// ResolveDesired の disabled 判定が override 判定より後になった場合、停止したグループが override の失効まで起動する
func TestDisabledBeatsUnexpiredOverride(t *testing.T) {
	now := time.Now()
	tag := model.GroupConfig{Name: "dev", Mode: model.ModeDisabled}
	o := &model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(time.Hour).Unix()}
	assert.Empty(t, resolve(t, tag, o, now), "disabled must beat an unexpired override")
}

// disabled は cron も同じく無効化する
// mode を disabled としても start_cron/stop_cron は spec に残るため、解決が fromSchedule へ到達した場合、cron の定める時間帯では running を返す
func TestDisabledBeatsSchedule(t *testing.T) {
	tag := businessHours()
	tag.Mode = model.ModeDisabled
	assert.Empty(t, resolve(t, tag, nil, jst(t, "2026-07-15 12:00")), "disabled must beat the schedule")
}

func TestPinned(t *testing.T) {
	tag := model.GroupConfig{Name: "dev", Mode: model.ModePinned, Desired: model.DesiredStopped}
	assert.Equal(t, model.DesiredStopped, resolve(t, tag, nil, time.Now()))
}

func TestOverrideBeatsPinned(t *testing.T) {
	now := time.Now()
	tag := model.GroupConfig{Name: "dev", Mode: model.ModePinned, Desired: model.DesiredStopped}
	o := &model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(time.Hour).Unix()}
	assert.Equal(t, model.DesiredRunning, resolve(t, tag, o, now), "unexpired override must win")
}

func TestExpiredOverrideIgnored(t *testing.T) {
	now := time.Now()
	tag := model.GroupConfig{Name: "dev", Mode: model.ModePinned, Desired: model.DesiredStopped}
	o := &model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(-time.Minute).Unix()}
	assert.Equal(t, model.DesiredStopped, resolve(t, tag, o, now), "expired override must be ignored")
}

func TestOverrideBeatsSchedule(t *testing.T) {
	now := jst(t, "2026-07-15 12:00") // 水曜、業務時間内
	o := &model.Override{Desired: model.DesiredStopped, ExpiresAt: now.Add(time.Hour).Unix()}
	assert.Equal(t, model.DesiredStopped, resolve(t, businessHours(), o, now), "override must beat schedule")
}

func TestScheduleBusinessHours(t *testing.T) {
	cases := []struct {
		at   string
		want model.DesiredState
	}{
		{"2026-07-15 12:00", model.DesiredRunning}, // 水曜の正午
		{"2026-07-15 08:59", model.DesiredStopped}, // 水曜、start の直前
		{"2026-07-15 09:00", model.DesiredRunning}, // ちょうど start の時刻
		{"2026-07-15 20:00", model.DesiredStopped}, // ちょうど stop の時刻
		{"2026-07-15 23:00", model.DesiredStopped}, // 水曜の夜
		{"2026-07-18 12:00", model.DesiredStopped}, // 土曜
		{"2026-07-20 09:30", model.DesiredRunning}, // 月曜の朝
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, resolve(t, businessHours(), nil, jst(t, tc.at)), tc.at)
	}
}

func TestScheduleOnlyStopCron(t *testing.T) {
	tag := businessHours()
	tag.StartCron = ""
	assert.Equal(t, model.DesiredStopped, resolve(t, tag, nil, jst(t, "2026-07-15 12:00")), "stop-only schedule must resolve stopped")
}

func TestScheduleOnlyStartCron(t *testing.T) {
	tag := businessHours()
	tag.StopCron = ""
	assert.Equal(t, model.DesiredRunning, resolve(t, tag, nil, jst(t, "2026-07-15 03:00")), "start-only schedule must resolve running")
}

func TestScheduleWithoutCronsErrors(t *testing.T) {
	tag := businessHours()
	tag.StartCron, tag.StopCron = "", ""
	_, err := ResolveDesired(tag, nil, time.Now(), "UTC")
	require.Error(t, err, "want error for schedule without crons")
}

func TestScheduleUsesDefaultTimezone(t *testing.T) {
	tag := businessHours()
	tag.Timezone = ""
	// 03:00 UTC = 12:00 JST であり、既定が UTC の場合は cron の時間帯の外となる
	now := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	got, err := ResolveDesired(tag, nil, now, "UTC")
	require.NoError(t, err)
	assert.Equal(t, model.DesiredStopped, got, "UTC default")

	got, err = ResolveDesired(tag, nil, now, tokyo)
	require.NoError(t, err)
	assert.Equal(t, model.DesiredRunning, got, "JST default")
}

func TestInvalidTimezoneErrors(t *testing.T) {
	tag := businessHours()
	tag.Timezone = "Not/AZone"
	_, err := ResolveDesired(tag, nil, time.Now(), "UTC")
	require.Error(t, err, "want error for invalid timezone")
}

// start と stop のいずれが不正な場合も、解決を中断しなければならない
// 不正な側を発火しなかったものとして扱った場合、desired state はもう一方のみで決まる
// stop_cron のみが不正な場合、停止すべきグループが起動したまま残ることになる
// 不正な cron はエラー本文で特定する
func TestInvalidCronErrors(t *testing.T) {
	cases := map[string]func(*model.GroupConfig){
		"start_cron": func(g *model.GroupConfig) { g.StartCron = "not a cron" },
		"stop_cron":  func(g *model.GroupConfig) { g.StopCron = "not a cron" },
	}
	for field, breakIt := range cases {
		t.Run(field, func(t *testing.T) {
			tag := businessHours()
			breakIt(&tag)

			_, err := ResolveDesired(tag, nil, jst(t, "2026-07-15 12:00"), "UTC")

			require.Error(t, err, "want error for invalid cron")
			assert.Contains(t, err.Error(), field, "どちらの cron を直せばよいか分かる文言でなければならない")
		})
	}
}

// start_cron と stop_cron が同一時刻の場合は stop へ解決する
// 両者が同じ壁時計時刻で発火する場合のみが、fromSchedule の lastStart.After(*lastStop) による同時刻の判定を通る経路である
func TestSameInstantStartStopTieResolvesStopped(t *testing.T) {
	tag := model.GroupConfig{
		Name: "dev", Mode: model.ModeSchedule,
		StartCron: "0 9 * * *", StopCron: "0 9 * * *", Timezone: tokyo,
	}
	assert.Equal(t, model.DesiredStopped, resolve(t, tag, nil, jst(t, "2026-07-15 09:00")), "same-instant tie must resolve stopped (fail-safe)")
}

// now と同時刻に失効する override は、失効済みとして扱わなければならない
// schedule.go は狭義の不等号 ExpiresAt > now.Unix() を用いるため、境界の時刻は override を適用しない
func TestOverrideExpiringExactlyNowIsExpired(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	tag := model.GroupConfig{Name: "dev", Mode: model.ModePinned, Desired: model.DesiredStopped}
	o := &model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Unix()}
	assert.Equal(t, model.DesiredStopped, resolve(t, tag, o, now), "override expiring exactly now must be ignored")
}

// 夏時間の切り替えにおいても、cron の解決は正しく行われなければならない
// 夏時間のない Asia/Tokyo のみでは検出できないため、夏時間を持つ America/New_York を用いる
//
// 以下のテストは、すべて UTC の絶対時刻を now として与える
// 壁時計時刻で与えた場合、その文字列を壁時計へ戻す実装、すなわち夏時間を無視する実装でも同じ結果となり、
// now をローカルの壁時計へ変換できているかを検証できないためである
// ここで与える時刻は、EST(-05:00) と EDT(-04:00) のいずれで解釈するかにより結果が変わるものを選ぶ
func utcAt(t *testing.T, value string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02 15:04", value)
	require.NoError(t, err)
	return ts
}

func nyBusinessHours() model.GroupConfig {
	return model.GroupConfig{
		Name: "dev", Mode: model.ModeSchedule,
		StartCron: "0 9 * * *", StopCron: "0 20 * * *", Timezone: "America/New_York",
	}
}

// 2026-03-08 の America/New_York では、02:00 EST から 03:00 EDT へ移行する
// 13:30Z を境に前日と当日で結果が異なることが、オフセットが -05:00 から -04:00 へ変化したことを示す
// 固定オフセットで解決する実装では、この 2 件の結果は一致し、いずれかが失敗する
func TestScheduleAcrossSpringForward(t *testing.T) {
	cases := []struct {
		at   string
		want model.DesiredState
		why  string
	}{
		{"2026-03-07 13:30", model.DesiredStopped, "前日、EST では 08:30 でまだ start 前"},
		{"2026-03-08 13:30", model.DesiredRunning, "切り替え後、EDT では 09:30 ですでに start 済み"},
		{"2026-03-08 06:30", model.DesiredStopped, "切り替え直前の 01:30 EST、前日の stop がまだ効いている"},
		{"2026-03-09 01:00", model.DesiredStopped, "翌日、EDT では 20:00 ちょうどで stop 側に倒す"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, resolve(t, nyBusinessHours(), nil, utcAt(t, tc.at)), "%s: %s", tc.at, tc.why)
	}
}

// 2026-11-01 の America/New_York では、02:00 EDT から 01:00 EST へ移行する
// 春と同じく、13:30Z を境に前日と当日で結果が異ならなければならない (-04:00 から -05:00 へ変化する)
func TestScheduleAcrossFallBack(t *testing.T) {
	cases := []struct {
		at   string
		want model.DesiredState
		why  string
	}{
		{"2026-10-31 13:30", model.DesiredRunning, "前日、EDT では 09:30 ですでに start 済み"},
		{"2026-11-01 13:30", model.DesiredStopped, "切り替え後、EST では 08:30 でまだ start 前"},
		{"2026-11-01 14:30", model.DesiredRunning, "同じ日の 09:30 EST では start 済み"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, resolve(t, nyBusinessHours(), nil, utcAt(t, tc.at)), "%s: %s", tc.at, tc.why)
	}
}

// 秋の繰り下げにおいて 2 回訪れる時刻に置いた cron は、その日 2 回発火しうる
// desired state は直近に発火した側のみで決まる冪等な導出であるため、2 回目の発火でも結果は変わってはならない
// 2026-11-01 のローカル 01:30 は、05:30Z (EDT) と 06:30Z (EST) の 2 回訪れる
func TestScheduleRepeatedHourFiresIdempotently(t *testing.T) {
	group := nyBusinessHours()
	group.StartCron = "30 1 * * *"

	cases := []struct {
		at   string
		want model.DesiredState
		why  string
	}{
		{"2026-11-01 05:00", model.DesiredStopped, "1 回目の 01:00 EDT、まだ start 前"},
		{"2026-11-01 05:31", model.DesiredRunning, "1 回目の 01:30 EDT で start 済み"},
		{"2026-11-01 06:31", model.DesiredRunning, "繰り下げ後の 2 回目の 01:30 EST でも running のまま"},
		{"2026-11-01 12:00", model.DesiredRunning, "同じ日の 07:00 EST でも変わらない"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, resolve(t, group, nil, utcAt(t, tc.at)), "%s: %s", tc.at, tc.why)
	}
}

// 春の繰り上げにより存在しなくなる時刻に置いた cron は、その日は発火しない (schedule.go の fromSchedule を参照)
// この挙動を仕様として固定する
// 解決の実装を変更したとき、この日の挙動のみが変化することを検出するためである
// 2026-03-08 の America/New_York に 02:30 は存在しない
func TestScheduleSkipsCronInMissingHour(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		// start が発火しない場合、そのグループはその日は停止したままとなる
		group := nyBusinessHours()
		group.StartCron = "30 2 * * *"

		assert.Equal(t, model.DesiredStopped, resolve(t, group, nil, utcAt(t, "2026-03-08 16:00")), "欠落した start はその日発火しない")
		assert.Equal(t, model.DesiredRunning, resolve(t, group, nil, utcAt(t, "2026-03-09 16:00")), "翌日は通常どおり start する")
	})

	t.Run("stop", func(t *testing.T) {
		// stop が発火しない場合、そのグループは次の stop まで起動したままとなる
		group := nyBusinessHours()
		group.StartCron, group.StopCron = "0 20 * * *", "30 2 * * *"

		assert.Equal(t, model.DesiredRunning, resolve(t, group, nil, utcAt(t, "2026-03-08 16:00")), "欠落した stop はその日発火しない")
		assert.Equal(t, model.DesiredStopped, resolve(t, group, nil, utcAt(t, "2026-03-09 08:00")), "翌日は通常どおり stop する")
	})
}
