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
func Run(ctx context.Context, _ json.RawMessage, deps *Deps, now time.Time) (summary Summary, runErr error) {
	log := deps.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	finishRun := startStep(ctx, log, "reconcile")
	defer func() { finishRun(runErr) }()

	finishLoadGroups := startStep(ctx, log, "load-groups")
	rows, err := deps.Store.ListGroups(ctx)
	finishLoadGroups(err, "groups", len(rows))
	if err != nil {
		return Summary{}, err
	}

	finishResolveDesired := startStep(ctx, log, "resolve-desired")
	type resolvedGroup struct {
		group   model.GroupSpec
		desired model.DesiredState
		err     error
	}
	groups := make(map[string]resolvedGroup, len(rows))
	summary = Summary{Actions: []Result{}, Errors: []Result{}}
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
	finishResolveDesired(nil, "groups", len(groups), "errors", len(summary.Errors))

	finishDiscovery := startStep(ctx, log, "discover-resources")
	discovered, err := deps.Discoverer.Discover(ctx)
	if err != nil {
		finishDiscovery(err)
		return Summary{}, fmt.Errorf("discover resources: %w", err)
	}
	resources := make([]model.Resource, 0, len(discovered))
	for _, resource := range discovered {
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].ARN < resources[j].ARN })
	for _, resource := range resources {
		log.DebugContext(ctx, "resource-discovered", "group", resource.Tags[model.GroupTagKey], "resource_id", resource.ID(), "resource_arn", resource.ARN, "resource_type", resource.Type)
	}
	finishDiscovery(nil, "resources", len(resources))

	finishResources := startStep(ctx, log, "reconcile-resources")
	for _, resource := range resources {
		groupName := resource.Tags[model.GroupTagKey]
		result := Result{Group: groupName, ResourceID: resource.ID()}
		info, _ := model.Info(resource.Type)
		resourceLog := log.With("group", groupName, "resource_id", result.ResourceID, "resource_arn", resource.ARN, "resource_type", resource.Type, "service", info.ARNService)
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
		result.Desired = group.desired
		if group.desired == model.DesiredNone {
			result.Skipped = "disabled"
			resourceLog.InfoContext(ctx, "resource-skipped", "desired", group.desired, "observed_before", nil, "observed_after", nil, "reason", result.Skipped)
			continue
		}

		target, ok := deps.Targets[resource.Type]
		if !ok {
			result.Error = fmt.Sprintf("resource %s has no target for type %q", resource.ARN, resource.Type)
			summary.Errors = append(summary.Errors, result)
			logResultError(log, result)
			continue
		}
		resource.EcsMaxCount = group.group.EcsMaxCount
		summary.Reconciled++
		finishDescribe := startStep(ctx, resourceLog, "describe-resource")
		observation, err := target.Describe(ctx, resource)
		finishDescribe(err, "observed", observation.State, "needs_start", observation.NeedsStart, "needs_stop", observation.NeedsStop, "detail", observation.Detail)
		if err != nil {
			result.Error = fmt.Sprintf("resource %s: describe: %v", resource.ARN, err)
			summary.Errors = append(summary.Errors, result)
			logResultError(log, result)
			continue
		}
		result.Observed = observation.State
		finishDecision := startStep(ctx, resourceLog, "decide-action", "desired", group.desired, "observed", observation.State)
		action := model.DecideAction(group.desired, observation.State)
		if action == model.ActionNone && group.desired == model.DesiredRunning && observation.NeedsStart {
			action = model.ActionStart
		}
		if action == model.ActionNone && group.desired == model.DesiredStopped && observation.NeedsStop {
			action = model.ActionStop
		}
		finishDecision(nil, "action", action)
		if action == model.ActionNone {
			if observation.State == model.StateTransitioning || observation.State == model.StateNotFound {
				result.Skipped = string(observation.State)
				resourceLog.InfoContext(ctx, "resource-skipped", "desired", group.desired, "observed_before", observation.State, "observed_after", observation.State, "reason", result.Skipped, "detail", observation.Detail)
			} else {
				resourceLog.InfoContext(ctx, "resource-unchanged", "desired", group.desired, "observed_before", observation.State, "observed_after", observation.State, "detail", observation.Detail)
			}
			continue
		}
		finishAction := startStep(ctx, resourceLog, "apply-action", "action", action)
		resourceLog.InfoContext(ctx, "action-started", "action", action, "desired", group.desired, "observed_before", observation.State, "detail_before", observation.Detail)
		actionErr := performAction(ctx, resource, action, target)
		finishAction(actionErr)

		finishDescribeAfter := startStep(ctx, resourceLog, "describe-resource-after-action")
		after, afterErr := target.Describe(ctx, resource)
		finishDescribeAfter(afterErr, "observed", after.State, "needs_start", after.NeedsStart, "needs_stop", after.NeedsStop, "detail", after.Detail)
		var observedAfter any
		if afterErr == nil {
			observedAfter = after.State
		}
		at := now.UTC().Format(time.RFC3339)
		actionArgs := []any{"action", action, "desired", group.desired, "observed_before", observation.State, "observed_after", observedAfter, "detail_before", observation.Detail, "detail_after", after.Detail, "success", actionErr == nil, "at", at}
		if actionErr != nil {
			actionArgs = append(actionArgs, "error", actionErr.Error())
		}
		if afterErr != nil {
			actionArgs = append(actionArgs, "observation_error", afterErr.Error())
		}
		resourceLog.InfoContext(ctx, "action-finished", actionArgs...)
		if afterErr != nil {
			observationResult := result
			observationResult.Error = fmt.Sprintf("resource %s: describe after %s: %v", resource.ARN, action, afterErr)
			summary.Errors = append(summary.Errors, observationResult)
			logResultError(log, observationResult)
		}
		if actionErr != nil {
			result.Error = fmt.Sprintf("resource %s: %s: %v", resource.ARN, action, actionErr)
			summary.Errors = append(summary.Errors, result)
			logResultError(log, result)
			continue
		}
		result.Action = action
		summary.Actions = append(summary.Actions, result)
		deliverActionNotification(ctx, deps.Notifier, log, groupName, result.ResourceID, action, group.desired, at)
	}
	finishResources(nil, "reconciled", summary.Reconciled, "actions", len(summary.Actions), "errors", len(summary.Errors))

	log.Info("summary", "reconciled", summary.Reconciled, "actions", len(summary.Actions), "errors", len(summary.Errors))
	return summary, nil
}

func startStep(ctx context.Context, log *slog.Logger, step string, args ...any) func(error, ...any) {
	stepLog := log.With(args...)
	stepLog.DebugContext(ctx, step+"-started")
	return func(err error, args ...any) {
		args = append(args, "success", err == nil)
		if err != nil {
			args = append(args, "error", err.Error())
		}
		stepLog.DebugContext(ctx, step+"-finished", args...)
	}
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
		finishNotification := startStep(ctx, log, "notify-action", "group", group, "resource_id", resourceID, "action", action, "attempt", attempt)
		err := notifier.Publish(ctx, subject, payload)
		finishNotification(err)
		if err == nil {
			return
		} else {
			log.Error("action-notify-failed", "group", group, "resource_id", resourceID, "attempt", attempt, "error", err.Error())
		}
	}
}

func logResultError(log *slog.Logger, result Result) {
	log.Error("resource-error", "group", result.Group, "resource_id", result.ResourceID, "error", result.Error)
}
