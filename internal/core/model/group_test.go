package model

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateGroup(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	validSchedule := GroupSpec{Name: "dev", StartCron: "0 9 * * MON-FRI", StopCron: "0 20 * * MON-FRI"}
	tests := map[string]struct {
		group   GroupSpec
		wantErr string
	}{
		"schedule":             {group: validSchedule},
		"indefinite override":  {group: GroupSpec{Name: "dev", Override: OverrideRunning}},
		"timed override":       {group: GroupSpec{Name: "dev", StartCron: "0 9 * * *", StopCron: "0 20 * * *", Override: OverrideStopped, OverrideExpiresAt: now.Add(time.Hour).Unix()}},
		"disabled override":    {group: GroupSpec{Name: "dev", Override: OverrideDisabled}},
		"missing stop":         {group: GroupSpec{Name: "dev", StartCron: "0 9 * * *"}, wantErr: "both be set"},
		"empty":                {group: GroupSpec{Name: "dev"}, wantErr: "schedule or override"},
		"unknown override":     {group: GroupSpec{Name: "dev", Override: "paused"}, wantErr: "override must"},
		"expiry without value": {group: GroupSpec{Name: "dev", StartCron: "0 9 * * *", StopCron: "0 20 * * *", OverrideExpiresAt: 1}, wantErr: "requires override"},
		"timed without cron":   {group: GroupSpec{Name: "dev", Override: OverrideRunning, OverrideExpiresAt: now.Add(time.Hour).Unix()}, wantErr: "requires a schedule"},
		"too large expiry":     {group: GroupSpec{Name: "dev", StartCron: "0 9 * * *", StopCron: "0 20 * * *", Override: OverrideRunning, OverrideExpiresAt: MaxOverrideExpiresAt + 1}, wantErr: "no greater"},
		"negative expiry":      {group: GroupSpec{Name: "dev", StartCron: "0 9 * * *", StopCron: "0 20 * * *", Override: OverrideRunning, OverrideExpiresAt: -1}, wantErr: "positive integer"},
		"six fields":           {group: GroupSpec{Name: "dev", StartCron: "0 0 9 * * *", StopCron: "0 20 * * *"}, wantErr: "5-field"},
		"seven fields":         {group: GroupSpec{Name: "dev", StartCron: "0 0 9 * * * 2026", StopCron: "0 20 * * *"}, wantErr: "5-field"},
		"unreachable":          {group: GroupSpec{Name: "dev", StartCron: "0 0 31 2 *", StopCron: "0 20 * * *"}, wantErr: "occurrence"},
		"leap day":             {group: GroupSpec{Name: "dev", StartCron: "0 0 29 2 *", StopCron: "0 20 * * *"}},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateGroup(test.group, now, time.UTC)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}

func TestValidateGroupForWriteRejectsExpiredOverride(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	group := GroupSpec{
		Name: "dev", StartCron: "0 9 * * *", StopCron: "0 20 * * *",
		Override: OverrideRunning, OverrideExpiresAt: now.Unix(),
	}
	require.NoError(t, ValidateGroup(group, now, time.UTC))
	assert.ErrorContains(t, ValidateGroupForWrite(group, now, time.UTC), "later than")
}

func TestValidGroupName(t *testing.T) {
	require.NoError(t, ValidGroupName("A.b_c-9"))
	require.NoError(t, ValidGroupName(strings.Repeat("a", 64)))
	assert.Error(t, ValidGroupName(""))
	assert.Error(t, ValidGroupName("bad/name"))
	assert.Error(t, ValidGroupName(strings.Repeat("a", 65)))
}
