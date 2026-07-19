// Package schedule resolves the desired state: override > pinned > cron.
package schedule

import (
	"fmt"
	"time"

	"github.com/adhocore/gronx"

	"cheapskate/internal/model"
)

// ResolveDesired returns "running" / "stopped", or "" when the resource is disabled.
func ResolveDesired(cfg model.Config, override *model.Override, now time.Time, defaultTimezone string) (string, error) {
	if cfg.Mode == model.ModeDisabled {
		return "", nil
	}
	if override != nil && override.ExpiresAt > now.Unix() {
		return override.Desired, nil
	}
	if cfg.Mode == model.ModePinned {
		return cfg.Desired, nil
	}
	return fromSchedule(cfg, now, defaultTimezone)
}

func fromSchedule(cfg model.Config, now time.Time, defaultTimezone string) (string, error) {
	tzName := cfg.Timezone
	if tzName == "" {
		tzName = defaultTimezone
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return "", fmt.Errorf("%s: invalid timezone %q: %w", cfg.ResourceID, tzName, err)
	}
	localNow := now.In(loc)

	lastStart, err := prev(cfg.StartCron, localNow)
	if err != nil {
		return "", fmt.Errorf("%s: start_cron: %w", cfg.ResourceID, err)
	}
	lastStop, err := prev(cfg.StopCron, localNow)
	if err != nil {
		return "", fmt.Errorf("%s: stop_cron: %w", cfg.ResourceID, err)
	}

	switch {
	case lastStart == nil && lastStop == nil:
		return "", fmt.Errorf("%s: mode=schedule requires start_cron and/or stop_cron", cfg.ResourceID)
	case lastStart == nil:
		return model.DesiredStopped, nil
	case lastStop == nil:
		return model.DesiredRunning, nil
	}
	// Whichever cron fired most recently decides; a tie means stop (fail safe/cheap).
	if lastStart.After(*lastStop) {
		return model.DesiredRunning, nil
	}
	return model.DesiredStopped, nil
}

func prev(expr string, localNow time.Time) (*time.Time, error) {
	if expr == "" {
		return nil, nil
	}
	t, err := gronx.PrevTickBefore(expr, localNow, true)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
