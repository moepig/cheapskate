package model

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/adhocore/gronx"
)

const MaxOverrideExpiresAt int64 = 253402300799

var groupNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ValidGroupName(name string) error {
	if !groupNameRE.MatchString(name) {
		return fmt.Errorf("invalid group name %q (must match %s)", name, groupNameRE.String())
	}
	return nil
}

type Override string

const (
	OverrideRunning  Override = "running"
	OverrideStopped  Override = "stopped"
	OverrideDisabled Override = "disabled"
)

func (o Override) Validate() error {
	switch o {
	case OverrideRunning, OverrideStopped, OverrideDisabled:
		return nil
	default:
		return fmt.Errorf("override must be running, stopped, or disabled, got %q", o)
	}
}

func ParseOverride(raw string) (Override, error) {
	o := Override(raw)
	return o, o.Validate()
}

type GroupSpec struct {
	Name              string
	StartCron         string
	StopCron          string
	Override          Override
	OverrideExpiresAt int64
}

type ScheduleSpec struct {
	StartCron string
	StopCron  string
}

func (g GroupSpec) HasSchedule() bool {
	return g.StartCron != "" || g.StopCron != ""
}

func (g GroupSpec) HasOverride() bool {
	return g.Override != ""
}

func (g GroupSpec) OverrideActive(now time.Time) bool {
	return g.HasOverride() && (g.OverrideExpiresAt == 0 || g.OverrideExpiresAt > now.Unix())
}

func ValidateGroup(g GroupSpec, reference time.Time, loc *time.Location) error {
	if err := ValidGroupName(g.Name); err != nil {
		return err
	}
	if loc == nil {
		return fmt.Errorf("group %s: timezone is required", g.Name)
	}
	if (g.StartCron == "") != (g.StopCron == "") {
		return fmt.Errorf("group %s: start_cron and stop_cron must both be set or both be omitted", g.Name)
	}
	if g.HasSchedule() {
		if err := validateCron("start_cron", g.StartCron, reference.In(loc)); err != nil {
			return fmt.Errorf("group %s: %w", g.Name, err)
		}
		if err := validateCron("stop_cron", g.StopCron, reference.In(loc)); err != nil {
			return fmt.Errorf("group %s: %w", g.Name, err)
		}
	}
	if g.HasOverride() {
		if err := g.Override.Validate(); err != nil {
			return fmt.Errorf("group %s: %w", g.Name, err)
		}
	}
	if g.OverrideExpiresAt != 0 {
		if !g.HasOverride() {
			return fmt.Errorf("group %s: override_expires_at requires override", g.Name)
		}
		if g.OverrideExpiresAt < 0 || g.OverrideExpiresAt > MaxOverrideExpiresAt {
			return fmt.Errorf("group %s: override_expires_at must be a positive integer no greater than %d", g.Name, MaxOverrideExpiresAt)
		}
		if !g.HasSchedule() {
			return fmt.Errorf("group %s: a timed override requires a schedule", g.Name)
		}
	}
	if !g.HasSchedule() && !g.HasOverride() {
		return fmt.Errorf("group %s: schedule or override is required", g.Name)
	}
	return nil
}

func ValidateGroupForWrite(g GroupSpec, now time.Time, loc *time.Location) error {
	if err := ValidateGroup(g, now, loc); err != nil {
		return err
	}
	if g.OverrideExpiresAt != 0 && g.OverrideExpiresAt <= now.Unix() {
		return fmt.Errorf("group %s: override_expires_at must be later than the write time", g.Name)
	}
	return nil
}

func validateCron(label, expr string, reference time.Time) error {
	if len(strings.Fields(expr)) != 5 || !gronx.IsValid(expr) {
		return fmt.Errorf("invalid %s expression %q: want a valid 5-field cron", label, expr)
	}
	if _, err := gronx.PrevTickBefore(expr, reference, true); err != nil {
		return fmt.Errorf("invalid %s expression %q: no previous occurrence: %w", label, expr, err)
	}
	if _, err := gronx.NextTickAfter(expr, reference, true); err != nil {
		return fmt.Errorf("invalid %s expression %q: no next occurrence: %w", label, expr, err)
	}
	return nil
}
