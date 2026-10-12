package schedule

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
)

func TestResolveDesired(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	base := model.GroupSpec{Name: "dev", StartCron: "0 9 * * *", StopCron: "0 20 * * *"}
	tests := map[string]struct {
		group model.GroupSpec
		want  model.DesiredState
	}{
		"schedule running":  {group: base, want: model.DesiredRunning},
		"running override":  {group: withOverride(base, model.OverrideRunning, 0), want: model.DesiredRunning},
		"stopped override":  {group: withOverride(base, model.OverrideStopped, 0), want: model.DesiredStopped},
		"disabled override": {group: withOverride(base, model.OverrideDisabled, 0), want: model.DesiredNone},
		"expired override":  {group: withOverride(base, model.OverrideStopped, now.Add(-time.Second).Unix()), want: model.DesiredRunning},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveDesired(test.group, now, time.UTC)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestResolveDesiredUsesGlobalTimezone(t *testing.T) {
	location, err := time.LoadLocation("Asia/Tokyo")
	require.NoError(t, err)
	group := model.GroupSpec{Name: "dev", StartCron: "0 9 * * *", StopCron: "0 20 * * *"}
	now := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	got, err := ResolveDesired(group, now, location)
	require.NoError(t, err)
	assert.Equal(t, model.DesiredRunning, got)
}

func TestResolveDesiredChoosesStoppedWhenTicksTie(t *testing.T) {
	group := model.GroupSpec{Name: "dev", StartCron: "0 9 * * *", StopCron: "0 9 * * *"}

	desired, err := ResolveDesired(group, time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC), time.UTC)

	require.NoError(t, err)
	assert.Equal(t, model.DesiredStopped, desired)
}

func TestResolveDesiredAtScheduleAndOverrideBoundaries(t *testing.T) {
	group := model.GroupSpec{Name: "dev", StartCron: "0 * * * *", StopCron: "30 * * * *"}
	for name, test := range map[string]struct {
		now  time.Time
		want model.DesiredState
	}{
		"before stop": {now: time.Date(2026, 9, 3, 12, 29, 59, 0, time.UTC), want: model.DesiredRunning},
		"at stop":     {now: time.Date(2026, 9, 3, 12, 30, 0, 0, time.UTC), want: model.DesiredStopped},
		"after stop":  {now: time.Date(2026, 9, 3, 12, 30, 1, 0, time.UTC), want: model.DesiredStopped},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveDesired(group, test.now, time.UTC)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}

	group.Override = model.OverrideStopped
	group.OverrideExpiresAt = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC).Unix()
	got, err := ResolveDesired(group, time.Date(2026, 9, 3, 12, 0, 0, 500_000_000, time.UTC), time.UTC)
	require.NoError(t, err)
	assert.Equal(t, model.DesiredRunning, got)
}

// UTC の基準時刻で停止後と重複時刻の状態を確認し、時刻の巻き戻りだけでは再起動しないことを検証する。
func TestResolveDesiredAcrossDaylightSavingTransitions(t *testing.T) {
	tests := []struct {
		name, zone, start, stop string
		states                  map[string]model.DesiredState
	}{
		{
			name: "stays stopped across fallback", zone: "America/New_York", start: "30 0 * * *", stop: "15 1 * * *",
			states: map[string]model.DesiredState{
				"2026-11-01T05:14:59Z": model.DesiredRunning,
				"2026-11-01T05:15:00Z": model.DesiredStopped,
				"2026-11-01T05:55:00Z": model.DesiredStopped,
				"2026-11-01T05:59:59Z": model.DesiredStopped,
				"2026-11-01T06:00:00Z": model.DesiredStopped,
				"2026-11-01T06:05:00Z": model.DesiredStopped,
				"2026-11-01T06:15:00Z": model.DesiredStopped,
			},
		},
		{
			name: "both occurrences can start and stop", zone: "America/New_York", start: "15 1 * * *", stop: "45 1 * * *",
			states: map[string]model.DesiredState{
				"2026-11-01T05:15:00Z": model.DesiredRunning,
				"2026-11-01T05:45:00Z": model.DesiredStopped,
				"2026-11-01T06:00:00Z": model.DesiredStopped,
				"2026-11-01T06:15:00Z": model.DesiredRunning,
				"2026-11-01T06:45:00Z": model.DesiredStopped,
			},
		},
		{
			name: "missing start does not fire", zone: "America/New_York", start: "30 2 * * *", stop: "30 3 * * *",
			states: map[string]model.DesiredState{
				"2026-03-08T06:59:59Z": model.DesiredStopped,
				"2026-03-08T07:10:00Z": model.DesiredStopped,
				"2026-03-08T07:30:00Z": model.DesiredStopped,
				"2026-03-09T06:30:00Z": model.DesiredRunning,
			},
		},
		{
			name: "half-hour fallback", zone: "Australia/Lord_Howe", start: "30 0 * * *", stop: "45 1 * * *",
			states: map[string]model.DesiredState{
				"2026-04-04T14:45:00Z": model.DesiredStopped,
				"2026-04-04T15:05:00Z": model.DesiredStopped,
			},
		},
		{
			name: "ties remain stopped across fallback", zone: "America/New_York", start: "15 1 * * *", stop: "15 1 * * *",
			states: map[string]model.DesiredState{
				"2026-11-01T05:15:00Z": model.DesiredStopped,
				"2026-11-01T06:05:00Z": model.DesiredStopped,
				"2026-11-01T06:15:00Z": model.DesiredStopped,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			location, err := time.LoadLocation(tt.zone)
			require.NoError(t, err)
			group := model.GroupSpec{Name: "dev", StartCron: tt.start, StopCron: tt.stop}
			for raw, want := range tt.states {
				t.Run(raw, func(t *testing.T) {
					now, err := time.Parse(time.RFC3339, raw)
					require.NoError(t, err)
					got, err := ResolveDesired(group, now, location)
					require.NoError(t, err)
					assert.Equal(t, want, got)
				})
			}
		})
	}
}

func withOverride(group model.GroupSpec, override model.Override, expiresAt int64) model.GroupSpec {
	group.Override, group.OverrideExpiresAt = override, expiresAt
	return group
}
