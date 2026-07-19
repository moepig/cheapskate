// Package ops implements the configuration operations shared by cheapskate-cli and the web console. Like both frontends, it only touches DynamoDB items — never the RDS/ECS APIs.
package ops

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/adhocore/gronx"

	"cheapskate/internal/model"
	"cheapskate/internal/store"
)

// MemberRow is one tag member with its status.
type MemberRow struct {
	ResourceID string
	Member     model.MemberItem
	Status     model.Status
}

// TagRow is one tag with its override and resolved members. Err is set when this tag's override or a member/status item was malformed (B-5); the row is still shown so the operator can see and fix it instead of the whole list disappearing.
type TagRow struct {
	Name     string
	Tag      model.TagItem
	Override *model.Override
	Members  []MemberRow
	Err      error
}

// List returns every registered tag with its override and members resolved, via one Scan pass (store.ScanAll) instead of a GetItem per tag/member (C-1). A tag name with members/override/status but no tag# item is orphaned data and is not listed here.
func List(ctx context.Context, s *store.Store, now time.Time) ([]TagRow, error) {
	scanRows, err := s.ScanAll(ctx, now)
	if err != nil {
		return nil, err
	}
	rows := make([]TagRow, 0, len(scanRows))
	for _, sr := range scanRows {
		if !sr.HasTag {
			continue
		}
		rows = append(rows, toTagRow(sr))
	}
	return rows, nil
}

// Get returns a single registered tag with its members resolved, or an error when unregistered.
func Get(ctx context.Context, s *store.Store, tag string, now time.Time) (TagRow, error) {
	if err := model.ValidTagName(tag); err != nil {
		return TagRow{}, err
	}
	scanRows, err := s.ScanAll(ctx, now)
	if err != nil {
		return TagRow{}, err
	}
	for _, sr := range scanRows {
		if sr.Name == tag {
			if !sr.HasTag {
				break
			}
			return toTagRow(sr), nil
		}
	}
	return TagRow{}, fmt.Errorf("tag %q is not registered", tag)
}

func toTagRow(sr store.TagRow) TagRow {
	members := make([]MemberRow, 0, len(sr.Members))
	for _, mr := range sr.Members {
		members = append(members, MemberRow{ResourceID: mr.ResourceID, Member: mr.Member, Status: mr.Status})
	}
	return TagRow{Name: sr.Name, Tag: sr.Tag, Override: sr.Override, Members: members, Err: sr.Err}
}

// AssembleResourceID validates a (type, name, cluster, service) combination as entered via the CLI
// or the web console's add form and assembles the internal "<type>#<identifier>" resource ID. The
// type token matches the storage prefix (model.ResourceTypes' keys), not the resolved
// model.Type* constant — e.g. "ecs", not "ecs-service".
func AssembleResourceID(typ, name, cluster, service string) (string, error) {
	switch typ {
	case "":
		return "", fmt.Errorf("--type is required (rds-instance | rds-cluster | ecs)")
	case "rds-instance", "rds-cluster":
		if name == "" {
			return "", fmt.Errorf("--type %s requires --name", typ)
		}
		if cluster != "" || service != "" {
			return "", fmt.Errorf("--cluster/--service apply only to --type ecs")
		}
		return typ + "#" + name, nil
	case "ecs":
		if cluster == "" || service == "" {
			return "", fmt.Errorf("--type ecs requires --cluster and --service")
		}
		if name != "" {
			return "", fmt.Errorf("--name applies only to rds-instance/rds-cluster; use --cluster/--service for ecs")
		}
		return "ecs#" + cluster + "/" + service, nil
	default:
		return "", fmt.Errorf("unknown --type %q (rds-instance | rds-cluster | ecs)", typ)
	}
}

// Add registers a resource as a member of a tag, creating the tag (mode=disabled) if it does not
// already exist. A resource may belong to only one tag: adding a resource already in a different
// tag is an error; re-adding it to the same tag upserts (restoreCount 0 preserves whatever
// restore_count was already stored, matching Schedule's B-9 semantics for crons).
func Add(ctx context.Context, s *store.Store, tag, resourceID string, restoreCount int) (tagCreated bool, err error) {
	if err := model.ValidTagName(tag); err != nil {
		return false, err
	}
	typ, err := model.ResourceIDType(resourceID)
	if err != nil {
		return false, err
	}
	if restoreCount != 0 && typ != model.TypeEcsService {
		return false, fmt.Errorf("restore count only applies to ecs resources")
	}

	existing, err := s.GetMember(ctx, resourceID)
	if err != nil {
		return false, err
	}
	if existing != nil && existing.Tag != tag {
		return false, fmt.Errorf("%s is already in tag %q (remove it first: cheapskate-cli remove --tag %s ...)", resourceID, existing.Tag, existing.Tag)
	}

	if t, err := s.GetTag(ctx, tag); err != nil {
		return false, err
	} else if t == nil {
		if err := s.PutTag(ctx, model.TagItem{PK: model.TagPrefix + tag, Mode: model.ModeDisabled}); err != nil {
			return false, err
		}
		tagCreated = true
	}

	item := model.MemberItem{PK: model.MemberPrefix + resourceID, Tag: tag, Type: typ}
	switch {
	case restoreCount > 0:
		count := int32(restoreCount)
		item.RestoreCount = &count
	case existing != nil:
		item.RestoreCount = existing.RestoreCount
	}

	if existing != nil {
		return tagCreated, s.PutMember(ctx, item)
	}
	if err := s.CreateMember(ctx, item); err != nil {
		if errors.Is(err, store.ErrMemberExists) {
			// Raced with a concurrent add between the GetMember check above and here.
			if m, gerr := s.GetMember(ctx, resourceID); gerr == nil && m != nil && m.Tag != tag {
				return tagCreated, fmt.Errorf("%s is already in tag %q", resourceID, m.Tag)
			}
			return tagCreated, nil
		}
		return tagCreated, err
	}
	return tagCreated, nil
}

// RemoveMember deletes a single member (and its status) from a tag, without touching the tag itself.
func RemoveMember(ctx context.Context, s *store.Store, tag, resourceID string) error {
	m, err := s.GetMember(ctx, resourceID)
	if err != nil {
		return err
	}
	if m == nil {
		return fmt.Errorf("%s is not a member of any tag", resourceID)
	}
	if m.Tag != tag {
		return fmt.Errorf("%s is in tag %q, not %q", resourceID, m.Tag, tag)
	}
	if err := s.Delete(ctx, model.MemberPrefix+resourceID); err != nil {
		return err
	}
	return s.Delete(ctx, model.StatusPrefix+resourceID)
}

// RemoveTag deletes a tag entirely: all its members, their statuses, its override, and the tag
// item itself. Members are deleted first so a failure partway through leaves the tag item (and
// any remaining members) reachable for a retry, rather than an orphaned tag-less member.
func RemoveTag(ctx context.Context, s *store.Store, tag string) error {
	members, err := s.ListMembers(ctx, tag)
	if err != nil {
		return err
	}
	for _, m := range members {
		resourceID := strings.TrimPrefix(m.PK, model.MemberPrefix)
		if err := s.Delete(ctx, model.MemberPrefix+resourceID); err != nil {
			return err
		}
		if err := s.Delete(ctx, model.StatusPrefix+resourceID); err != nil {
			return err
		}
	}
	if err := s.Delete(ctx, model.OverridePrefix+tag); err != nil {
		return err
	}
	return s.Delete(ctx, model.TagPrefix+tag)
}

// Pin sets mode=pinned with the given desired state on an existing tag. Cron fields are kept; they are inert under mode=pinned and restorable via Schedule.
func Pin(ctx context.Context, s *store.Store, tag, desired string) error {
	if err := ValidDesired(desired); err != nil {
		return err
	}
	existing, err := requireTag(ctx, s, tag)
	if err != nil {
		return err
	}
	item := *existing
	item.Mode, item.Desired = model.ModePinned, desired
	return s.PutTag(ctx, item)
}

// ScheduleSpec are the arguments to Schedule.
type ScheduleSpec struct {
	StartCron string
	StopCron  string
	Timezone  string
}

// Schedule sets mode=schedule with the given crons on an existing tag and returns the written item.
func Schedule(ctx context.Context, s *store.Store, tag string, spec ScheduleSpec) (model.TagItem, error) {
	if spec.StartCron == "" && spec.StopCron == "" {
		return model.TagItem{}, fmt.Errorf("schedule requires a start and/or stop cron")
	}
	for _, expr := range []string{spec.StartCron, spec.StopCron} {
		if expr != "" && !gronx.IsValid(expr) {
			return model.TagItem{}, fmt.Errorf("invalid cron expression %q", expr)
		}
	}
	if spec.Timezone != "" {
		if _, err := time.LoadLocation(spec.Timezone); err != nil {
			return model.TagItem{}, fmt.Errorf("invalid timezone %q", spec.Timezone)
		}
	}
	if _, err := requireTag(ctx, s, tag); err != nil {
		return model.TagItem{}, err
	}
	item := model.TagItem{
		PK:        model.TagPrefix + tag,
		Mode:      model.ModeSchedule,
		StartCron: spec.StartCron,
		StopCron:  spec.StopCron,
		Timezone:  spec.Timezone,
	}
	if err := s.PutTag(ctx, item); err != nil {
		return model.TagItem{}, err
	}
	return item, nil
}

// Disable sets mode=disabled, keeping the rest of the tag config.
func Disable(ctx context.Context, s *store.Store, tag string) error {
	existing, err := requireTag(ctx, s, tag)
	if err != nil {
		return err
	}
	existing.Mode = model.ModeDisabled
	return s.PutTag(ctx, *existing)
}

// SetOverride writes a time-limited override for a tag and returns its expiry.
func SetOverride(ctx context.Context, s *store.Store, tag, desired string, d time.Duration, now time.Time) (time.Time, error) {
	if err := ValidDesired(desired); err != nil {
		return time.Time{}, err
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("override duration must be positive")
	}
	existing, err := requireTag(ctx, s, tag)
	if err != nil {
		return time.Time{}, err
	}
	// B-6: disabled is a stronger stop than override — the reconciler skips disabled tags before it ever looks at the override, so accepting one here would silently do nothing.
	if existing.Mode == model.ModeDisabled {
		return time.Time{}, fmt.Errorf("tag %q is disabled; disabled overrides mode=schedule/pinned but is itself not overridable (schedule or pin it first)", tag)
	}
	expiresAt := now.Add(d)
	if err := s.PutOverride(ctx, tag, model.Override{Desired: desired, ExpiresAt: expiresAt.Unix()}); err != nil {
		return time.Time{}, err
	}
	return expiresAt, nil
}

// ClearOverride removes the override item now (instead of waiting for TTL).
func ClearOverride(ctx context.Context, s *store.Store, tag string) error {
	return s.Delete(ctx, model.OverridePrefix+tag)
}

// requireTag fetches and validates an existing tag, since Pin/Schedule/Disable/SetOverride must
// not silently create one from a typo — unlike Add, which creates on first use by design.
func requireTag(ctx context.Context, s *store.Store, tag string) (*model.TagItem, error) {
	if err := model.ValidTagName(tag); err != nil {
		return nil, err
	}
	existing, err := s.GetTag(ctx, tag)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("tag %q not found (create it by adding a member: cheapskate-cli add --tag %s ...)", tag, tag)
	}
	return existing, nil
}

// ValidDesired checks a desired-state argument.
func ValidDesired(desired string) error {
	if desired != model.DesiredRunning && desired != model.DesiredStopped {
		return fmt.Errorf("desired state must be running or stopped, got %q", desired)
	}
	return nil
}
