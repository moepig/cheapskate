package schedule

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
)

const tokyo = "Asia/Tokyo"

// 与えられた JST の壁時計時刻に対応する UTC の瞬間を返す
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

// disabled は override より強い停止である
// disable は override# アイテムを消さないので、pin → override → disable の順に操作すれば「disabled なグループに有効期限内の override が残っている」状態は普通に作れる
// ResolveDesired の disabled 判定が override 判定より後ろへ動くと、止めたはずのグループが期限切れまで起動しはじめる
func TestDisabledBeatsUnexpiredOverride(t *testing.T) {
	now := time.Now()
	tag := model.GroupConfig{Name: "dev", Mode: model.ModeDisabled}
	o := &model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(time.Hour).Unix()}
	assert.Empty(t, resolve(t, tag, o, now), "disabled must beat an unexpired override")
}

// disabled は cron も同じく無効化する
// mode を disabled にしても start_cron/stop_cron は spec に残るため、解決が誤って fromSchedule まで進むと業務時間中は running が返ってしまう
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
	// 03:00 UTC = 12:00 JST であり、既定が UTC なら業務時間外になる
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

// start と stop のどちらが壊れていても解決を中断しなければならない
// 壊れた側を「発火しなかった」とみなすと、もう片方だけで desired が決まってしまう
// 例えば stop_cron が壊れているだけで、止めるはずのグループが一日中動き続けることになる
// どちらの cron が悪いのかはエラー本文で名指しする
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

// start_cron と stop_cron が同一時刻で並んだ場合は stop に解決する（安全側かつ安価側）
// 両者が同じ壁時計時刻で発火する状況だけが、fromSchedule の lastStart.After(*lastStop) の同着判定を実際に通す唯一の経路である
func TestSameInstantStartStopTieResolvesStopped(t *testing.T) {
	tag := model.GroupConfig{
		Name: "dev", Mode: model.ModeSchedule,
		StartCron: "0 9 * * *", StopCron: "0 9 * * *", Timezone: tokyo,
	}
	assert.Equal(t, model.DesiredStopped, resolve(t, tag, nil, jst(t, "2026-07-15 09:00")), "same-instant tie must resolve stopped (fail-safe)")
}

// ちょうど now に失効する override は、すでに失効したものとして扱わなければならない
// schedule.go は狭義の不等号 ExpiresAt > now.Unix() を使うため、境界の瞬間そのものは勝ってはならない
func TestOverrideExpiringExactlyNowIsExpired(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	tag := model.GroupConfig{Name: "dev", Mode: model.ModePinned, Desired: model.DesiredStopped}
	o := &model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Unix()}
	assert.Equal(t, model.DesiredStopped, resolve(t, tag, o, now), "override expiring exactly now must be ignored")
}

// 夏時間の切り替えで cron の解決が壊れてはならない
// DST のない Asia/Tokyo だけを試しても検出できないため、DST を持つ America/New_York を使う
//
// 以下のテストはすべて UTC の絶対時刻を now として与える
// 壁時計時刻で書くと、その文字列を壁時計へ戻す実装（＝ DST を無視する実装）でも同じ答えになってしまい、「now を正しくローカルの壁時計へ写しているか」を一切検証できないためである
// ここで与える瞬間は、EST(-05:00) と EDT(-04:00) のどちらで解釈するかで答えが変わるものを選んである
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

// 2026-03-08 の America/New_York では 02:00 EST から 03:00 EDT へ一気に進む
// 13:30Z を挟んで前日と当日で答えが割れることが、オフセットが実際に -05:00 から -04:00 へ動いた証拠になる
// 固定オフセットで解く実装ではこの 2 件は同じ答えになり、必ずどちらかが落ちる
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

// 2026-11-01 の America/New_York では 02:00 EDT から 01:00 EST へ戻る
// 春と同じく、13:30Z を挟んで前日と当日で答えが割れなければならない（-04:00 から -05:00 へ動く）
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

// 秋の繰り下げで 2 回訪れる時刻に置いた cron は、その日 2 回発火しうる
// desired state は「直近に発火した側」だけで決まる冪等な導出なので、2 回目の発火でも答えは変わってはならない
// 2026-11-01 のローカル 01:30 は 05:30Z（EDT）と 06:30Z（EST）の 2 回訪れる
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

// 春の繰り上げで消える時刻に置いた cron は、その日まるごと発火しない（schedule.go の fromSchedule を参照）
// 望ましい挙動ではないが実際の挙動なので、仕様として固定しておく
// 誰かが解決を作り替えたときに、この日だけ黙って挙動が変わることを防ぐ
// 2026-03-08 の America/New_York に 02:30 は存在しない
func TestScheduleSkipsCronInMissingHour(t *testing.T) {
	t.Run("start", func(t *testing.T) {
		// start が飛ぶと、そのグループはその日ずっと停止したままになる（安価側）
		group := nyBusinessHours()
		group.StartCron = "30 2 * * *"

		assert.Equal(t, model.DesiredStopped, resolve(t, group, nil, utcAt(t, "2026-03-08 16:00")), "欠落した start はその日発火しない")
		assert.Equal(t, model.DesiredRunning, resolve(t, group, nil, utcAt(t, "2026-03-09 16:00")), "翌日は通常どおり start する")
	})

	t.Run("stop", func(t *testing.T) {
		// stop が飛ぶと、そのグループは次の stop まで動き続ける（高価側）
		group := nyBusinessHours()
		group.StartCron, group.StopCron = "0 20 * * *", "30 2 * * *"

		assert.Equal(t, model.DesiredRunning, resolve(t, group, nil, utcAt(t, "2026-03-08 16:00")), "欠落した stop はその日発火しない")
		assert.Equal(t, model.DesiredStopped, resolve(t, group, nil, utcAt(t, "2026-03-09 08:00")), "翌日は通常どおり stop する")
	})
}
