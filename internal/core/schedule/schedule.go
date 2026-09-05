package schedule

import (
	"fmt"
	"time"

	"github.com/adhocore/gronx"

	"cheapskate/internal/core/model"
)

func ResolveDesired(group model.GroupSpec, now time.Time, loc *time.Location) (model.DesiredState, error) {
	if err := model.ValidateGroup(group, now, loc); err != nil {
		return model.DesiredNone, err
	}
	if group.OverrideActive(now) {
		switch group.Override {
		case model.OverrideDisabled:
			return model.DesiredNone, nil
		case model.OverrideRunning:
			return model.DesiredRunning, nil
		case model.OverrideStopped:
			return model.DesiredStopped, nil
		}
	}
	if !group.HasSchedule() {
		return model.DesiredNone, fmt.Errorf("group %s: expired override has no schedule", group.Name)
	}
	localNow := now.In(loc)
	lastStart, err := gronx.PrevTickBefore(group.StartCron, localNow, true)
	if err != nil {
		return model.DesiredNone, fmt.Errorf("group %s: start_cron: %w", group.Name, err)
	}
	lastStop, err := gronx.PrevTickBefore(group.StopCron, localNow, true)
	if err != nil {
		return model.DesiredNone, fmt.Errorf("group %s: stop_cron: %w", group.Name, err)
	}
	if lastStart.After(lastStop) {
		return model.DesiredRunning, nil
	}
	return model.DesiredStopped, nil
}
