// Package ops implements the configuration operations shared by csctl and the web console. Like both frontends, it only touches DynamoDB items — never the RDS/ECS APIs.
package ops

import (
	"context"
	"fmt"
	"time"

	"github.com/adhocore/gronx"

	"cheapskate/internal/model"
	"cheapskate/internal/store"
)

// Row is one resource in the list view: config joined with its override and status. Err is set when this row's override or status item was malformed (B-5); the row is still shown so the operator can see and fix it instead of the whole list disappearing.
type Row struct {
	ResourceID string
	Config     model.ConfigItem
	Override   *model.Override
	Status     model.Status
	Err        error
}

// List returns every registered resource with override and status joined, via one Scan pass (store.ScanAll) instead of a GetItem per resource (C-1). A resource_id with an override or status item but no config is orphaned data and is not listed here.
func List(ctx context.Context, s *store.Store, now time.Time) ([]Row, error) {
	scanRows, err := s.ScanAll(ctx, now)
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, len(scanRows))
	for _, sr := range scanRows {
		if !sr.HasConfig {
			continue
		}
		rows = append(rows, Row{ResourceID: sr.ResourceID, Config: sr.Config, Override: sr.Override, Status: sr.Status, Err: sr.Err})
	}
	return rows, nil
}

// Get returns a single registered resource, or an error when unregistered.
func Get(ctx context.Context, s *store.Store, resourceID string, now time.Time) (Row, error) {
	cfg, err := s.GetConfig(ctx, resourceID)
	if err != nil {
		return Row{}, err
	}
	if cfg == nil {
		return Row{}, fmt.Errorf("%s is not registered", resourceID)
	}
	override, err := s.GetOverride(ctx, resourceID, now)
	if err != nil {
		return Row{}, err
	}
	status, err := s.GetStatus(ctx, resourceID)
	if err != nil {
		return Row{}, err
	}
	return Row{ResourceID: resourceID, Config: *cfg, Override: override, Status: status}, nil
}

// Pin sets mode=pinned with the given desired state. Cron fields of an existing config are kept; they are inert under mode=pinned and restorable via Schedule.
func Pin(ctx context.Context, s *store.Store, resourceID, desired string) error {
	if err := ValidDesired(desired); err != nil {
		return err
	}
	resourceType, err := model.ResourceIDType(resourceID)
	if err != nil {
		return err
	}
	item := model.ConfigItem{
		PK:      model.ConfigPrefix + resourceID,
		Type:    resourceType,
		Mode:    model.ModePinned,
		Desired: desired,
	}
	if existing, err := s.GetConfig(ctx, resourceID); err != nil {
		return err
	} else if existing != nil {
		item.StartCron, item.StopCron = existing.StartCron, existing.StopCron
		item.Timezone, item.RestoreCount = existing.Timezone, existing.RestoreCount
	}
	return s.PutConfig(ctx, item)
}

// ScheduleSpec are the arguments to Schedule. RestoreCount 0 means "leave whatever the existing config already has" (B-9) — to clear it explicitly, remove the resource and re-register it.
type ScheduleSpec struct {
	StartCron    string
	StopCron     string
	Timezone     string
	RestoreCount int
}

// Schedule sets mode=schedule with the given crons and returns the written item.
func Schedule(ctx context.Context, s *store.Store, resourceID string, spec ScheduleSpec) (model.ConfigItem, error) {
	if spec.StartCron == "" && spec.StopCron == "" {
		return model.ConfigItem{}, fmt.Errorf("schedule requires a start and/or stop cron")
	}
	for _, expr := range []string{spec.StartCron, spec.StopCron} {
		if expr != "" && !gronx.IsValid(expr) {
			return model.ConfigItem{}, fmt.Errorf("invalid cron expression %q", expr)
		}
	}
	if spec.Timezone != "" {
		if _, err := time.LoadLocation(spec.Timezone); err != nil {
			return model.ConfigItem{}, fmt.Errorf("invalid timezone %q", spec.Timezone)
		}
	}
	resourceType, err := model.ResourceIDType(resourceID)
	if err != nil {
		return model.ConfigItem{}, err
	}
	if spec.RestoreCount != 0 && resourceType != model.TypeEcsService {
		return model.ConfigItem{}, fmt.Errorf("restore count only applies to ecs# resources")
	}
	item := model.ConfigItem{
		PK:        model.ConfigPrefix + resourceID,
		Type:      resourceType,
		Mode:      model.ModeSchedule,
		StartCron: spec.StartCron,
		StopCron:  spec.StopCron,
		Timezone:  spec.Timezone,
	}
	if spec.RestoreCount > 0 {
		count := int32(spec.RestoreCount)
		item.RestoreCount = &count
	} else if existing, err := s.GetConfig(ctx, resourceID); err != nil {
		return model.ConfigItem{}, err
	} else if existing != nil {
		// B-9: re-running schedule without -restore-count must not silently drop it, the way it would if Schedule always overwrote the whole item.
		item.RestoreCount = existing.RestoreCount
	}
	if err := s.PutConfig(ctx, item); err != nil {
		return model.ConfigItem{}, err
	}
	return item, nil
}

// Disable sets mode=disabled, keeping the rest of the config.
func Disable(ctx context.Context, s *store.Store, resourceID string) error {
	existing, err := s.GetConfig(ctx, resourceID)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("%s is not registered", resourceID)
	}
	existing.Mode = model.ModeDisabled
	return s.PutConfig(ctx, *existing)
}

// SetOverride writes a time-limited override and returns its expiry.
func SetOverride(ctx context.Context, s *store.Store, resourceID, desired string, d time.Duration, now time.Time) (time.Time, error) {
	if err := ValidDesired(desired); err != nil {
		return time.Time{}, err
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("override duration must be positive")
	}
	existing, err := s.GetConfig(ctx, resourceID)
	if err != nil {
		return time.Time{}, err
	}
	if existing == nil {
		return time.Time{}, fmt.Errorf("%s is not registered (an override without a config has no effect)", resourceID)
	}
	// B-6: disabled is a stronger stop than override — ReconcileOne skips disabled resources before it ever looks at the override, so accepting one here would silently do nothing.
	if existing.Mode == model.ModeDisabled {
		return time.Time{}, fmt.Errorf("%s is disabled; disabled overrides mode=schedule/pinned but is itself not overridable (schedule or pin it first)", resourceID)
	}
	expiresAt := now.Add(d)
	if err := s.PutOverride(ctx, resourceID, model.Override{Desired: desired, ExpiresAt: expiresAt.Unix()}); err != nil {
		return time.Time{}, err
	}
	return expiresAt, nil
}

// ClearOverride removes the override item now (instead of waiting for TTL).
func ClearOverride(ctx context.Context, s *store.Store, resourceID string) error {
	return s.Delete(ctx, model.OverridePrefix+resourceID)
}

// Remove deletes config, override, and status.
func Remove(ctx context.Context, s *store.Store, resourceID string) error {
	for _, prefix := range []string{model.ConfigPrefix, model.OverridePrefix, model.StatusPrefix} {
		if err := s.Delete(ctx, prefix+resourceID); err != nil {
			return err
		}
	}
	return nil
}

// ValidDesired checks a desired-state argument.
func ValidDesired(desired string) error {
	if desired != model.DesiredRunning && desired != model.DesiredStopped {
		return fmt.Errorf("desired state must be running or stopped, got %q", desired)
	}
	return nil
}
