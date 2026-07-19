// Package schedule resolves the desired state: override > pinned > cron.
package schedule

import (
	"fmt"
	"time"

	"github.com/adhocore/gronx"

	"cheapskate/internal/model"
)

// ResolveDesired returns "running" / "stopped", or "" when the tag is disabled.
func ResolveDesired(tag model.TagConfig, override *model.Override, now time.Time, defaultTimezone string) (string, error) {
	if tag.Mode == model.ModeDisabled {
		return "", nil
	}
	if override != nil && override.ExpiresAt > now.Unix() {
		return override.Desired, nil
	}
	if tag.Mode == model.ModePinned {
		return tag.Desired, nil
	}
	return fromSchedule(tag, now, defaultTimezone)
}

func fromSchedule(tag model.TagConfig, now time.Time, defaultTimezone string) (string, error) {
	tzName := tag.Timezone
	if tzName == "" {
		tzName = defaultTimezone
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return "", fmt.Errorf("tag %s: invalid timezone %q: %w", tag.Name, tzName, err)
	}
	localNow := now.In(loc)

	lastStart, err := prev(tag.StartCron, localNow)
	if err != nil {
		return "", fmt.Errorf("tag %s: start_cron: %w", tag.Name, err)
	}
	lastStop, err := prev(tag.StopCron, localNow)
	if err != nil {
		return "", fmt.Errorf("tag %s: stop_cron: %w", tag.Name, err)
	}

	switch {
	case lastStart == nil && lastStop == nil:
		return "", fmt.Errorf("tag %s: mode=schedule requires start_cron and/or stop_cron", tag.Name)
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
