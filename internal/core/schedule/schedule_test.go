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

func withOverride(group model.GroupSpec, override model.Override, expiresAt int64) model.GroupSpec {
	group.Override, group.OverrideExpiresAt = override, expiresAt
	return group
}
