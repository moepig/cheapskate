package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"cheapskate/internal/app/port"
	"cheapskate/internal/core/model"
	"cheapskate/internal/core/schedule"
	"cheapskate/internal/state"
)

type Store interface {
	ListGroups(ctx context.Context) ([]state.GroupRow, error)
}

type Deps struct {
	Store      Store
	Discoverer port.Discoverer
	Targets    map[model.ResourceType]port.Target
	Notifier   port.Notifier
	Location   *time.Location
	Log        *slog.Logger
}

type Result struct {
	Group      string              `json:"group,omitempty"`
	ResourceID string              `json:"resource_id,omitempty"`
	Desired    model.DesiredState  `json:"desired,omitempty"`
	Observed   model.ObservedState `json:"observed,omitempty"`
	Action     model.Action        `json:"action,omitempty"`
	Skipped    string              `json:"skipped,omitempty"`
	Error      string              `json:"error,omitempty"`
}

type Summary struct {
	Reconciled int      `json:"reconciled"`
	Actions    []Result `json:"actions"`
	Errors     []Result `json:"errors"`
}

const maxNotificationAttempts = 2

// Run は payload の内容にかかわらず full reconcile を実行する。
func Run(ctx context.Context, _ json.RawMessage, deps *Deps, now time.Time) (Summary, error) {
	log := deps.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	rows, err := deps.Store.ListGroups(ctx)
	if err != nil {
		return Summary{}, err
	}

	type resolvedGroup struct {
		group   model.GroupSpec
		desired model.DesiredState
		err     error
	}
	groups := make(map[string]resolvedGroup, len(rows))
	summary := Summary{Actions: []Result{}, Errors: []Result{}}
	for _, row := range rows {
		resolved := resolvedGroup{group: row.Group, err: row.Err}
		if resolved.err == nil {
			resolved.desired, resolved.err = schedule.ResolveDesired(row.Group, now, deps.Location)
		}
		groups[row.Name] = resolved
		if resolved.err != nil {
			result := Result{Group: row.Name, Error: resolved.err.Error()}
			summary.Errors = append(summary.Errors, result)
			log.Error("group-error", "group", row.Name, "error", resolved.err.Error())
		}
	}

	discovered, err := deps.Discoverer.Discover(ctx)
	if err != nil {
		return Summary{}, fmt.Errorf("discover resources: %w", err)
	}
	resources := make([]model.Resource, 0, len(discovered))
	for _, resource := range discovered {
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].ARN < resources[j].ARN })

	for _, resource := range resources {
		groupName := resource.Tags[model.GroupTagKey]
		result := Result{Group: groupName, ResourceID: resource.ID()}
		if groupName == "" {
			result.Error = fmt.Sprintf("resource %s has an empty %s tag", resource.ARN, model.GroupTagKey)
			summary.Errors = append(summary.Errors, result)
			logResultError(log, result)
			continue
		}
		if err := model.ValidGroupName(groupName); err != nil {
			result.Error = fmt.Sprintf("resource %s: %v", resource.ARN, err)
			summary.Errors = append(summary.Errors, result)
			logResultError(log, result)
			continue
		}
		group, ok := groups[groupName]
		if !ok {
			result.Error = fmt.Sprintf("resource %s references unknown group %q", resource.ARN, groupName)
			summary.Errors = append(summary.Errors, result)
			logResultError(log, result)
			continue
		}
		if group.err != nil {
			result.Error = fmt.Sprintf("resource %s references invalid group %q", resource.ARN, groupName)
			summary.Errors = append(summary.Errors, result)
			logResultError(log, result)
			continue
		}
		if group.desired == model.DesiredNone {
			result.Skipped = "disabled"
			log.Info("resource-skipped", "group", groupName, "resource_id", result.ResourceID, "reason", result.Skipped)
			continue
		}

		target, ok := deps.Targets[resource.Type]
		if !ok {
			result.Error = fmt.Sprintf("resource %s has no target for type %q", resource.ARN, resource.Type)
			summary.Errors = append(summary.Errors, result)
			logResultError(log, result)
			continue
		}
		summary.Reconciled++
		observation, err := target.Describe(ctx, resource)
		if err != nil {
			result.Error = fmt.Sprintf("resource %s: describe: %v", resource.ARN, err)
			summary.Errors = append(summary.Errors, result)
			logResultError(log, result)
			continue
		}
		result.Desired = group.desired
		result.Observed = observation.State
		action := model.DecideAction(group.desired, observation.State)
		if action == model.ActionNone && group.desired == model.DesiredRunning && observation.NeedsStart {
			action = model.ActionStart
		}
		if action == model.ActionNone {
			if observation.State == model.StateTransitioning || observation.State == model.StateNotFound {
				result.Skipped = string(observation.State)
				log.Info("resource-skipped", "group", groupName, "resource_id", result.ResourceID, "reason", result.Skipped, "detail", observation.Detail)
			}
			continue
		}
		if err := performAction(ctx, resource, action, target); err != nil {
			result.Error = fmt.Sprintf("resource %s: %s: %v", resource.ARN, action, err)
			summary.Errors = append(summary.Errors, result)
			logResultError(log, result)
			continue
		}
		result.Action = action
		summary.Actions = append(summary.Actions, result)
		at := now.UTC().Format(time.RFC3339)
		log.Info("action", "group", groupName, "resource_id", result.ResourceID, "action", action, "desired", group.desired, "at", at)
		deliverActionNotification(ctx, deps.Notifier, log, groupName, result.ResourceID, action, group.desired, at)
	}

	log.Info("summary", "reconciled", summary.Reconciled, "actions", len(summary.Actions), "errors", len(summary.Errors))
	return summary, nil
}

func performAction(ctx context.Context, resource model.Resource, action model.Action, target port.Target) error {
	switch action {
	case model.ActionStart:
		return target.Start(ctx, resource)
	case model.ActionStop:
		return target.Stop(ctx, resource)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

func deliverActionNotification(ctx context.Context, notifier port.Notifier, log *slog.Logger, group, resourceID string, action model.Action, desired model.DesiredState, at string) {
	if notifier == nil {
		return
	}
	payload := map[string]any{
		"group": group, "resource_id": resourceID, "action": action, "desired": desired, "at": at,
	}
	subject := fmt.Sprintf("[cheapskate] %s: %s/%s", action, group, resourceID)
	for attempt := 1; attempt <= maxNotificationAttempts; attempt++ {
		if err := notifier.Publish(ctx, subject, payload); err == nil {
			return
		} else {
			log.Error("action-notify-failed", "group", group, "resource_id", resourceID, "attempt", attempt, "error", err.Error())
		}
	}
}

func logResultError(log *slog.Logger, result Result) {
	log.Error("resource-error", "group", result.Group, "resource_id", result.ResourceID, "error", result.Error)
}
