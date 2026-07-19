package schedule

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/model"
)

const tokyo = "Asia/Tokyo"

// jst returns a UTC instant corresponding to the given JST wall-clock time.
func jst(t *testing.T, value string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation(tokyo)
	require.NoError(t, err)
	ts, err := time.ParseInLocation("2006-01-02 15:04", value, loc)
	require.NoError(t, err)
	return ts.UTC()
}

func businessHours() model.Config {
	return model.Config{
		ResourceID: "ecs#dev/api",
		Type:       model.TypeEcsService,
		Mode:       model.ModeSchedule,
		StartCron:  "0 9 * * MON-FRI",
		StopCron:   "0 20 * * MON-FRI",
		Timezone:   tokyo,
	}
}

func resolve(t *testing.T, cfg model.Config, o *model.Override, now time.Time) string {
	t.Helper()
	got, err := ResolveDesired(cfg, o, now, "UTC")
	require.NoError(t, err)
	return got
}

func TestDisabledReturnsEmpty(t *testing.T) {
	cfg := model.Config{ResourceID: "rds-instance#db", Mode: model.ModeDisabled}
	assert.Empty(t, resolve(t, cfg, nil, time.Now()), "disabled must resolve to empty")
}

func TestPinned(t *testing.T) {
	cfg := model.Config{ResourceID: "rds-instance#db", Mode: model.ModePinned, Desired: model.DesiredStopped}
	assert.Equal(t, model.DesiredStopped, resolve(t, cfg, nil, time.Now()))
}

func TestOverrideBeatsPinned(t *testing.T) {
	now := time.Now()
	cfg := model.Config{ResourceID: "rds-instance#db", Mode: model.ModePinned, Desired: model.DesiredStopped}
	o := &model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(time.Hour).Unix()}
	assert.Equal(t, model.DesiredRunning, resolve(t, cfg, o, now), "unexpired override must win")
}

func TestExpiredOverrideIgnored(t *testing.T) {
	now := time.Now()
	cfg := model.Config{ResourceID: "rds-instance#db", Mode: model.ModePinned, Desired: model.DesiredStopped}
	o := &model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(-time.Minute).Unix()}
	assert.Equal(t, model.DesiredStopped, resolve(t, cfg, o, now), "expired override must be ignored")
}

func TestOverrideBeatsSchedule(t *testing.T) {
	now := jst(t, "2026-07-15 12:00") // Wednesday, inside business hours
	o := &model.Override{Desired: model.DesiredStopped, ExpiresAt: now.Add(time.Hour).Unix()}
	assert.Equal(t, model.DesiredStopped, resolve(t, businessHours(), o, now), "override must beat schedule")
}

func TestScheduleBusinessHours(t *testing.T) {
	cases := []struct {
		at   string
		want string
	}{
		{"2026-07-15 12:00", model.DesiredRunning}, // Wed noon
		{"2026-07-15 08:59", model.DesiredStopped}, // Wed before start
		{"2026-07-15 09:00", model.DesiredRunning}, // exactly at start
		{"2026-07-15 20:00", model.DesiredStopped}, // exactly at stop
		{"2026-07-15 23:00", model.DesiredStopped}, // Wed night
		{"2026-07-18 12:00", model.DesiredStopped}, // Saturday
		{"2026-07-20 09:30", model.DesiredRunning}, // Monday morning
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, resolve(t, businessHours(), nil, jst(t, tc.at)), tc.at)
	}
}

func TestScheduleOnlyStopCron(t *testing.T) {
	cfg := businessHours()
	cfg.StartCron = ""
	assert.Equal(t, model.DesiredStopped, resolve(t, cfg, nil, jst(t, "2026-07-15 12:00")), "stop-only schedule must resolve stopped")
}

func TestScheduleOnlyStartCron(t *testing.T) {
	cfg := businessHours()
	cfg.StopCron = ""
	assert.Equal(t, model.DesiredRunning, resolve(t, cfg, nil, jst(t, "2026-07-15 03:00")), "start-only schedule must resolve running")
}

func TestScheduleWithoutCronsErrors(t *testing.T) {
	cfg := businessHours()
	cfg.StartCron, cfg.StopCron = "", ""
	_, err := ResolveDesired(cfg, nil, time.Now(), "UTC")
	require.Error(t, err, "want error for schedule without crons")
}

func TestScheduleUsesDefaultTimezone(t *testing.T) {
	cfg := businessHours()
	cfg.Timezone = ""
	// 03:00 UTC = 12:00 JST; with default UTC it is outside business hours.
	now := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	got, err := ResolveDesired(cfg, nil, now, "UTC")
	require.NoError(t, err)
	assert.Equal(t, model.DesiredStopped, got, "UTC default")

	got, err = ResolveDesired(cfg, nil, now, tokyo)
	require.NoError(t, err)
	assert.Equal(t, model.DesiredRunning, got, "JST default")
}

func TestInvalidTimezoneErrors(t *testing.T) {
	cfg := businessHours()
	cfg.Timezone = "Not/AZone"
	_, err := ResolveDesired(cfg, nil, time.Now(), "UTC")
	require.Error(t, err, "want error for invalid timezone")
}

func TestInvalidCronErrors(t *testing.T) {
	cfg := businessHours()
	cfg.StartCron = "not a cron"
	_, err := ResolveDesired(cfg, nil, jst(t, "2026-07-15 12:00"), "UTC")
	require.Error(t, err, "want error for invalid cron")
}

// A-5: DESIGN.md's decision summary says a same-instant tie between start_cron and stop_cron resolves to stop (fail-safe/cheap). Both firing at the same wall-clock time is the only way to actually exercise the tie-break in fromSchedule's lastStart.After(*lastStop) check.
func TestSameInstantStartStopTieResolvesStopped(t *testing.T) {
	cfg := model.Config{
		ResourceID: "ecs#dev/api", Type: model.TypeEcsService, Mode: model.ModeSchedule,
		StartCron: "0 9 * * *", StopCron: "0 9 * * *", Timezone: tokyo,
	}
	assert.Equal(t, model.DesiredStopped, resolve(t, cfg, nil, jst(t, "2026-07-15 09:00")), "same-instant tie must resolve stopped (fail-safe)")
}

// A-5: an override expiring at exactly `now` must be treated as already expired — schedule.go uses ExpiresAt > now.Unix(), a strict inequality, so the boundary instant itself must not win.
func TestOverrideExpiringExactlyNowIsExpired(t *testing.T) {
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	cfg := model.Config{ResourceID: "rds-instance#db", Mode: model.ModePinned, Desired: model.DesiredStopped}
	o := &model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Unix()}
	assert.Equal(t, model.DesiredStopped, resolve(t, cfg, o, now), "override expiring exactly now must be ignored")
}

// A-9: DST transitions must not break cron resolution. Only testing Asia/Tokyo (no DST) can't catch this; America/New_York observes DST, so exercise both the spring-forward gap and the fall-back overlap.
func nyTime(t *testing.T, value string) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	ts, err := time.ParseInLocation("2006-01-02 15:04", value, loc)
	require.NoError(t, err)
	return ts.UTC()
}

func nyBusinessHours() model.Config {
	return model.Config{
		ResourceID: "ecs#dev/api", Type: model.TypeEcsService, Mode: model.ModeSchedule,
		StartCron: "0 9 * * *", StopCron: "0 20 * * *", Timezone: "America/New_York",
	}
}

func TestScheduleAcrossSpringForward(t *testing.T) {
	// 2026-03-08: America/New_York clocks jump from 01:59 EST straight to 03:00 EDT; 02:30 does not exist that day.
	cases := []struct {
		at   string
		want string
	}{
		{"2026-03-07 12:00", model.DesiredRunning}, // day before, normal business hours
		{"2026-03-08 12:00", model.DesiredRunning}, // the transition day itself, well after the gap
		{"2026-03-09 08:00", model.DesiredStopped}, // day after, before start
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, resolve(t, nyBusinessHours(), nil, nyTime(t, tc.at)), tc.at)
	}
}

func TestScheduleAcrossFallBack(t *testing.T) {
	// 2026-11-01: America/New_York clocks fall from 01:59 EDT back to 01:00 EST, so 01:30 occurs twice.
	cases := []struct {
		at   string
		want string
	}{
		{"2026-10-31 12:00", model.DesiredRunning}, // day before
		{"2026-11-01 12:00", model.DesiredRunning}, // the transition day itself, well after the overlap
		{"2026-11-02 08:00", model.DesiredStopped}, // day after, before start
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, resolve(t, nyBusinessHours(), nil, nyTime(t, tc.at)), tc.at)
	}
}
