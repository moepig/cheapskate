// Package reconcile holds the reconcile loop: full reconcile on schedule, scoped reconcile on RDS events.
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"cheapskate/internal/model"
	"cheapskate/internal/schedule"
	"cheapskate/internal/store"
	"cheapskate/internal/target"
)

var rdsSourceTypeToPrefix = map[string]string{
	"DB_INSTANCE": "rds-instance",
	"CLUSTER":     "rds-cluster",
}

//go:generate go tool mockgen -destination ../mocks/reconcile.go -package mocks cheapskate/internal/reconcile Notifier,SnsAPI

// Notifier publishes on actions and failures only.
type Notifier interface {
	Publish(ctx context.Context, subject string, payload map[string]any) error
}

// Deps is the dependency container for one reconcile run.
type Deps struct {
	Store           *store.Store
	Targets         map[string]target.Target
	Notifier        Notifier
	DefaultTimezone string
	Log             *slog.Logger
}

// Result is the outcome for one member of a tag.
type Result struct {
	Tag        string `json:"tag,omitempty"`
	ResourceID string `json:"resource_id,omitempty"`
	Desired    string `json:"desired,omitempty"`
	Observed   string `json:"observed,omitempty"`
	Action     string `json:"action,omitempty"`
	Skipped    string `json:"skipped,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Summary is the handler return value.
type Summary struct {
	Reconciled int      `json:"reconciled"`
	Actions    []Result `json:"actions"`
	Errors     []Result `json:"errors"`
}

// Event is the loosely-typed invocation payload: `{}` (or any non-RDS JSON object) triggers a full reconcile; an EventBridge RDS event reconciles only the resource it names.
type Event struct {
	Source string `json:"source"`
	Detail struct {
		SourceType       string `json:"SourceType"`
		SourceIdentifier string `json:"SourceIdentifier"`
	} `json:"detail"`
}

// Run dispatches on the event payload and reconciles.
func Run(ctx context.Context, raw json.RawMessage, deps *Deps, now time.Time) (Summary, error) {
	var event Event
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &event); err != nil {
			return Summary{}, fmt.Errorf("unmarshal event: %w", err)
		}
	}

	var results []Result
	if event.Source == "aws.rds" {
		results = runForRdsEvent(ctx, event, deps, now)
	} else {
		if event.Source != "" {
			// Any non-RDS, non-empty source falls back to a full reconcile today, but that's
			// almost certainly not what the caller intended (C-4). `{}` invocations (manual
			// trigger, EventBridge schedule with no input) are the only intentional case.
			deps.Log.Warn("unexpected-event-source", "source", event.Source, "reason", "falling back to full reconcile")
		}
		rows, err := deps.Store.ScanAll(ctx, now)
		if err != nil {
			return Summary{}, err
		}
		for _, row := range rows {
			if !row.HasTag {
				deps.Log.Warn("orphaned-tag-data", "tag", row.Name, "members", len(row.Members))
				continue
			}
			results = append(results, ReconcileTag(ctx, row, deps, now)...)
		}
	}

	summary := Summary{Reconciled: len(results), Actions: []Result{}, Errors: []Result{}}
	for _, r := range results {
		if r.Action != "" {
			summary.Actions = append(summary.Actions, r)
		}
		if r.Error != "" {
			summary.Errors = append(summary.Errors, r)
		}
	}
	deps.Log.Info("summary",
		"reconciled", summary.Reconciled,
		"actions", len(summary.Actions),
		"errors", len(summary.Errors))
	return summary, nil
}

func runForRdsEvent(ctx context.Context, event Event, deps *Deps, now time.Time) []Result {
	resourceID := RdsEventResourceID(event)
	if resourceID == "" {
		deps.Log.Info("ignored-event", "reason", "unrecognized RDS event shape")
		return nil
	}
	mi, err := deps.Store.GetMember(ctx, resourceID)
	if err != nil {
		return []Result{{ResourceID: resourceID, Error: err.Error()}}
	}
	if mi == nil {
		deps.Log.Info("ignored-event", "resource_id", resourceID, "reason", "not registered")
		return nil
	}
	ti, err := deps.Store.GetTag(ctx, mi.Tag)
	if err != nil {
		return []Result{{Tag: mi.Tag, ResourceID: resourceID, Error: err.Error()}}
	}
	if ti == nil {
		err := fmt.Errorf("member %s references missing tag %q", resourceID, mi.Tag)
		return []Result{{Tag: mi.Tag, ResourceID: resourceID, Error: err.Error()}}
	}
	override, err := deps.Store.GetOverride(ctx, mi.Tag, now)
	if err != nil {
		return []Result{{Tag: mi.Tag, ResourceID: resourceID, Error: err.Error()}}
	}
	// Reuse the tag loop with a single-member synthetic row, so an event touches only the one
	// resource it names even when its tag has other members.
	row := store.TagRow{
		Name: mi.Tag, Tag: *ti, HasTag: true, Override: override,
		Members: []store.MemberRow{{ResourceID: resourceID, Member: *mi}},
	}
	return ReconcileTag(ctx, row, deps, now)
}

// RdsEventResourceID maps an RDS EventBridge event to a resource_id, or "" when the event shape is unrecognized.
func RdsEventResourceID(event Event) string {
	prefix := rdsSourceTypeToPrefix[event.Detail.SourceType]
	if prefix == "" || event.Detail.SourceIdentifier == "" {
		return ""
	}
	return prefix + "#" + event.Detail.SourceIdentifier
}

// ReconcileTag converges every member of one tag. A tag-level failure (bad mode/cron/timezone/
// override) is recorded and notified per member via the same recordFailure path a per-member
// failure uses, so the notify-once dedup (B-3) and cheapskate-cli-visible last_error keep working
// with zero new bookkeeping. A member failure is recorded and notified but never propagated, so
// one member cannot break the rest of the tag.
func ReconcileTag(ctx context.Context, row store.TagRow, deps *Deps, now time.Time) []Result {
	desired, tagErr := resolveTag(row, deps, now)
	results := make([]Result, 0, len(row.Members))
	for _, mr := range row.Members {
		result := Result{Tag: row.Name, ResourceID: mr.ResourceID}
		switch {
		case tagErr != nil:
			result.Error = tagErr.Error()
			recordFailure(ctx, deps, row.Name, mr.ResourceID, tagErr, now)
		case desired == "":
			result.Skipped = "disabled"
		default:
			if err := reconcileMember(ctx, deps, row.Name, mr.Member, desired, now, &result); err != nil {
				result.Error = err.Error()
				recordFailure(ctx, deps, row.Name, mr.ResourceID, err, now)
			}
		}
		results = append(results, result)
	}
	return results
}

func resolveTag(row store.TagRow, deps *Deps, now time.Time) (string, error) {
	if row.Err != nil { // malformed override/member/status item under this tag (B-5)
		return "", row.Err
	}
	tag, err := model.ParseTag(row.Tag)
	if err != nil {
		return "", err
	}
	if tag.Mode == model.ModeDisabled {
		return "", nil
	}
	return schedule.ResolveDesired(tag, row.Override, now, deps.DefaultTimezone)
}

// reconcileMember does the work for one member: resolve target, compare desired to observed state, act if they differ, persist, notify. Any step failing aborts and the caller records it — no partial state beyond what performAction already wrote write-ahead.
func reconcileMember(ctx context.Context, deps *Deps, tagName string, memberItem model.MemberItem, desired string, now time.Time, result *Result) error {
	member, err := model.ParseMember(memberItem)
	if err != nil {
		return err
	}
	resourceID := member.ResourceID

	// Read once and reuse for the action (if any) and for recovery detection (B-11) below — one extra GetItem per cycle buys knowing whether this resource was previously erroring.
	status, err := deps.Store.GetStatus(ctx, resourceID)
	if err != nil {
		return err
	}

	tgt, ok := deps.Targets[member.Type]
	if !ok {
		return fmt.Errorf("no target for type %q", member.Type)
	}
	obs, err := tgt.Describe(ctx, member.Ref())
	if err != nil {
		return err
	}
	result.Desired, result.Observed = desired, obs.State

	if obs.State == model.StateTransitioning {
		deps.Log.Info("skip-transitioning", "resource_id", resourceID, "detail", obs.Detail)
		result.Skipped = "transitioning"
		return nil
	}
	if obs.State == model.StateNotFound {
		return fmt.Errorf("resource not found: %s", resourceID)
	}

	action := decideAction(desired, obs.State)
	if action == "" {
		// Converged: no action write. Still clear a stale error and notify recovery once (B-11) — this is the only place that happens, since a successful action's own notification already tells the user things are fine.
		clearRecoveredError(ctx, deps, tagName, resourceID, status, true, now)
		return nil // no write, no action notification
	}

	if err := performAction(ctx, deps, resourceID, action, tgt, member, status); err != nil {
		return err
	}
	if err := deps.Store.PutStatus(ctx, resourceID, map[string]any{
		"observed_state": obs.State,
		"last_action":    action,
		"last_action_at": now.UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	result.Action = action
	deps.Log.Info("action", "tag", tagName, "resource_id", resourceID, "action", action, "desired", desired)

	notifyAction(ctx, deps, tagName, resourceID, action, desired, now)
	clearRecoveredError(ctx, deps, tagName, resourceID, status, false, now)
	return nil
}

func decideAction(desired, observedState string) string {
	switch {
	case desired == model.DesiredStopped && observedState == model.StateRunning:
		return "stop"
	case desired == model.DesiredRunning && observedState == model.StateStopped:
		return "start"
	default:
		return ""
	}
}

// performAction runs the AWS-side mutation. For stop, target state needed to restore later is fetched and persisted (write-ahead) before the mutating call, so a crash between the two leaves a safe, restorable status item rather than none at all (B-1).
func performAction(ctx context.Context, deps *Deps, resourceID, action string, tgt target.Target, member model.Member, status model.Status) error {
	ref := member.Ref()
	switch action {
	case "stop":
		saved, err := tgt.PrepareStop(ctx, ref, member, status)
		if err != nil {
			return err
		}
		if attrs := store.SavedStateAttrs(saved); len(attrs) > 0 {
			if err := deps.Store.PutStatus(ctx, resourceID, attrs); err != nil {
				return err
			}
		}
		return tgt.Stop(ctx, ref, member, status)
	case "start":
		_, err := tgt.Start(ctx, ref, member, status)
		return err
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

// notifyAction is best-effort: a notification failure is logged, never treated as a reconcile error (B-4). The action already succeeded and is already persisted.
func notifyAction(ctx context.Context, deps *Deps, tag, resourceID, action, desired string, now time.Time) {
	if err := deps.Notifier.Publish(ctx,
		fmt.Sprintf("[cheapskate] %s: %s/%s", action, tag, resourceID),
		map[string]any{"tag": tag, "resource_id": resourceID, "action": action, "desired": desired, "at": now.UTC().Format(time.RFC3339)},
	); err != nil {
		deps.Log.Error("notify-failed", "tag", tag, "resource_id", resourceID, "error", err.Error())
	}
}

// clearRecoveredError clears a previously recorded error once a cycle completes without one (B-11). notify controls whether a distinct "recovered" notification is sent — the caller skips it after a successful action, since the action notification already conveys recovery.
func clearRecoveredError(ctx context.Context, deps *Deps, tag, resourceID string, prevStatus model.Status, notify bool, now time.Time) {
	if prevStatus.LastError == "" {
		return
	}
	if err := deps.Store.PutStatus(ctx, resourceID, map[string]any{"last_error": "", "last_error_at": ""}); err != nil {
		deps.Log.Error("error-clear-failed", "tag", tag, "resource_id", resourceID, "error", err.Error())
		return
	}
	if !notify {
		return
	}
	if err := deps.Notifier.Publish(ctx,
		fmt.Sprintf("[cheapskate] recovered: %s/%s", tag, resourceID),
		map[string]any{"tag": tag, "resource_id": resourceID, "at": now.UTC().Format(time.RFC3339)},
	); err != nil {
		deps.Log.Error("notify-failed", "tag", tag, "resource_id", resourceID, "error", err.Error())
	}
}

// recordFailure persists the error unconditionally but only notifies when it differs from the previously recorded one (B-3) — an ongoing not-found or access-denied error would otherwise page every cycle forever.
func recordFailure(ctx context.Context, deps *Deps, tag, resourceID string, err error, now time.Time) {
	deps.Log.Error("error", "tag", tag, "resource_id", resourceID, "error", err.Error())
	at := now.UTC().Format(time.RFC3339)

	prevStatus, gerr := deps.Store.GetStatus(ctx, resourceID)
	if gerr != nil {
		deps.Log.Error("error-reporting-failed", "tag", tag, "resource_id", resourceID, "error", gerr.Error())
	}

	if serr := deps.Store.PutStatus(ctx, resourceID, map[string]any{"last_error": err.Error(), "last_error_at": at}); serr != nil {
		deps.Log.Error("error-reporting-failed", "tag", tag, "resource_id", resourceID, "error", serr.Error())
	}

	if gerr == nil && prevStatus.LastError == err.Error() {
		return
	}
	if nerr := deps.Notifier.Publish(ctx,
		fmt.Sprintf("[cheapskate] error: %s/%s", tag, resourceID),
		map[string]any{"tag": tag, "resource_id": resourceID, "error": err.Error(), "at": at},
	); nerr != nil {
		deps.Log.Error("error-reporting-failed", "tag", tag, "resource_id", resourceID, "error", nerr.Error())
	}
}
