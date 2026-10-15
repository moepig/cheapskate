package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/app/port"
	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
)

func TestDebugLogsIncludeStepsAndAllDiscoveredResources(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{
		{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideStopped}},
		{Name: "off", Group: model.GroupSpec{Name: "off", Override: model.OverrideDisabled}},
	}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"a": taggedResource("a", "db", "dev"),
		"b": taggedResource("b", "disabled", "off"),
		"c": taggedResource("c", "unknown", "missing-group"),
	}
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["db"] = model.Observation{State: model.StateRunning}
	deps := testDeps(store, discoverer, target)
	var output bytes.Buffer
	deps.Log = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))

	summary, err := Run(context.Background(), nil, deps, time.Now())

	require.NoError(t, err)
	assert.Equal(t, 1, summary.Reconciled)
	records := readLogRecords(t, &output)
	var discoveredARNs []string
	for _, record := range records {
		if record["msg"] != "resource-discovered" {
			continue
		}
		assert.Equal(t, "DEBUG", record["level"])
		arn := record["resource_arn"].(string)
		discoveredARNs = append(discoveredARNs, arn)
		resource := discoverer.Resources[arn]
		assert.Equal(t, resource.ID(), record["resource_id"])
		assert.Equal(t, string(resource.Type), record["resource_type"])
		assert.Equal(t, resource.Tags[model.GroupTagKey], record["group"])
	}
	assert.Equal(t, []string{"a", "b", "c"}, discoveredARNs)
	for _, step := range []string{"reconcile", "load-groups", "resolve-desired", "discover-resources", "reconcile-resources", "describe-resource", "decide-action", "apply-action", "describe-resource-after-action", "notify-action"} {
		start := requireLogRecord(t, records, step+"-started", "level", "DEBUG")
		finish := requireLogRecord(t, records, step+"-finished", "level", "DEBUG")
		assert.Equal(t, "DEBUG", start["level"])
		assert.Equal(t, "DEBUG", finish["level"])
		assert.Equal(t, true, finish["success"])
	}
	assert.Equal(t, float64(3), requireLogRecord(t, records, "discover-resources-finished", "level", "DEBUG")["resources"])
	for _, record := range records {
		assert.NotEqual(t, "step-started", record["msg"])
		assert.NotEqual(t, "step-finished", record["msg"])
		assert.NotContains(t, record, "step")
	}
}

func TestFailedStepsAreFinishedWithTheError(t *testing.T) {
	for _, step := range []string{"load-groups", "discover-resources", "describe-resource", "apply-action", "describe-resource-after-action", "notify-action"} {
		t.Run(step, func(t *testing.T) {
			failure := errors.New("operation unavailable")
			store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideStopped}}}}
			discoverer := porttest.NewDiscoverer()
			discoverer.Resources["db"] = taggedResource("db", "db", "dev")
			target := porttest.NewTarget(model.TypeRdsInstance)
			target.Observations["db"] = model.Observation{State: model.StateRunning}
			deps := testDeps(store, discoverer, target)
			switch step {
			case "load-groups":
				store.err = failure
			case "discover-resources":
				discoverer.Err = failure
			case "describe-resource":
				target.DescribeErr = failure
			case "apply-action":
				target.StopErr = failure
			case "describe-resource-after-action":
				deps.Targets[target.Type()] = &afterActionTarget{Target: target, afterErr: failure}
			case "notify-action":
				deps.Notifier.(*porttest.Notifier).Err = failure
			}
			var output bytes.Buffer
			deps.Log = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))

			_, err := Run(context.Background(), nil, deps, time.Now())

			globalFailure := step == "load-groups" || step == "discover-resources"
			if globalFailure {
				assert.ErrorIs(t, err, failure)
			} else {
				assert.NoError(t, err)
			}
			records := readLogRecords(t, &output)
			requireLogRecord(t, records, step+"-started", "level", "DEBUG")
			finish := requireLogRecord(t, records, step+"-finished", "level", "DEBUG")
			assert.Equal(t, false, finish["success"])
			assert.Equal(t, failure.Error(), finish["error"])
			assert.Equal(t, !globalFailure, requireLogRecord(t, records, "reconcile-finished", "level", "DEBUG")["success"])
			if step == "notify-action" {
				var attempts []float64
				for _, record := range records {
					if record["msg"] == step+"-finished" {
						attempts = append(attempts, record["attempt"].(float64))
					}
				}
				assert.Equal(t, []float64{1, 2}, attempts)
			}
		})
	}
}

type actionLogTarget struct {
	port.Target
	beforeAction func()
}

func (target actionLogTarget) Start(ctx context.Context, resource model.Resource) error {
	target.beforeAction()
	return target.Target.Start(ctx, resource)
}

func (target actionLogTarget) Stop(ctx context.Context, resource model.Resource) error {
	target.beforeAction()
	return target.Target.Stop(ctx, resource)
}

func TestInfoLogsIdentifyActionsBeforeTheyAreApplied(t *testing.T) {
	for _, action := range []model.Action{model.ActionStart, model.ActionStop} {
		t.Run(string(action), func(t *testing.T) {
			override, observed := model.OverrideRunning, model.StateStopped
			if action == model.ActionStop {
				override, observed = model.OverrideStopped, model.StateRunning
			}
			store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: override}}}}
			discoverer := porttest.NewDiscoverer()
			resource := taggedResource("arn:db", "db", "dev")
			discoverer.Resources[resource.ARN] = resource
			target := porttest.NewTarget(model.TypeRdsInstance)
			target.Observations["db"] = model.Observation{State: observed}
			target.StartErr, target.StopErr = errors.New("action failed"), errors.New("action failed")
			deps := testDeps(store, discoverer, target)
			var output bytes.Buffer
			deps.Log = slog.New(slog.NewJSONHandler(&output, nil))
			called := false
			deps.Targets[resource.Type] = actionLogTarget{Target: target, beforeAction: func() {
				called = true
				record := requireLogRecord(t, readLogRecords(t, &output), "action-started", "resource_id", resource.ID())
				assert.Equal(t, "INFO", record["level"])
				assert.Equal(t, string(action), record["action"])
				assert.Equal(t, "dev", record["group"])
				assert.Equal(t, resource.ARN, record["resource_arn"])
				assert.Equal(t, string(resource.Type), record["resource_type"])
				assert.Equal(t, "rds", record["service"])
				assert.Equal(t, string(observed), record["observed_before"])
			}}

			summary, err := Run(context.Background(), nil, deps, time.Now())

			require.NoError(t, err)
			assert.True(t, called)
			assert.Empty(t, summary.Actions)
			assert.Len(t, summary.Errors, 1)
			finished := requireLogRecord(t, readLogRecords(t, &output), "action-finished", "resource_id", resource.ID())
			assert.Equal(t, false, finished["success"])
			assert.Equal(t, "action failed", finished["error"])
			for _, record := range readLogRecords(t, &output) {
				assert.NotEqual(t, "DEBUG", record["level"])
				assert.NotEqual(t, "action", record["msg"])
			}
		})
	}
}

type afterActionTarget struct {
	*porttest.Target
	after     model.Observation
	afterErr  error
	attempted bool
}

func (target *afterActionTarget) Describe(ctx context.Context, resource model.Resource) (model.Observation, error) {
	before, err := target.Target.Describe(ctx, resource)
	if target.attempted {
		return target.after, target.afterErr
	}
	return before, err
}

func (target *afterActionTarget) Start(ctx context.Context, resource model.Resource) error {
	target.attempted = true
	return target.Target.Start(ctx, resource)
}

func (target *afterActionTarget) Stop(ctx context.Context, resource model.Resource) error {
	target.attempted = true
	return target.Target.Stop(ctx, resource)
}

func TestInfoLogsRecordActualStatesBeforeAndAfterActions(t *testing.T) {
	for _, test := range []struct {
		name      string
		typ       model.ResourceType
		override  model.Override
		before    model.Observation
		after     model.Observation
		actionErr error
		afterErr  error
	}{
		{name: "RDS start transitioning", typ: model.TypeRdsInstance, override: model.OverrideRunning, before: model.Observation{State: model.StateStopped, Detail: "stopped"}, after: model.Observation{State: model.StateTransitioning, Detail: "starting"}},
		{name: "Aurora stop", typ: model.TypeRdsCluster, override: model.OverrideStopped, before: model.Observation{State: model.StateRunning, Detail: "available"}, after: model.Observation{State: model.StateStopped, Detail: "stopped"}},
		{name: "EC2 stop transitioning", typ: model.TypeEc2Instance, override: model.OverrideStopped, before: model.Observation{State: model.StateRunning, Detail: "running"}, after: model.Observation{State: model.StateTransitioning, Detail: "stopping"}},
		{name: "ECS repair", typ: model.TypeEcsService, override: model.OverrideRunning, before: model.Observation{State: model.StateRunning, Detail: "ScheduledScalingSuspended=true", NeedsStart: true}, after: model.Observation{State: model.StateRunning, Detail: "ScheduledScalingSuspended=false"}},
		{name: "partial failure", typ: model.TypeEcsService, override: model.OverrideRunning, before: model.Observation{State: model.StateStopped}, after: model.Observation{State: model.StateRunning}, actionErr: errors.New("response lost")},
		{name: "after observation failure", typ: model.TypeEc2Instance, override: model.OverrideRunning, before: model.Observation{State: model.StateStopped}, afterErr: errors.New("describe unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: test.override}}}}
			discoverer := porttest.NewDiscoverer()
			resource := model.Resource{Type: test.typ, ARN: "arn:resource", Ref: "resource", Tags: map[string]string{model.GroupTagKey: "dev"}}
			discoverer.Resources[resource.ARN] = resource
			base := porttest.NewTarget(test.typ)
			base.Observations[resource.Ref] = test.before
			base.StartErr, base.StopErr = test.actionErr, test.actionErr
			target := &afterActionTarget{Target: base, after: test.after, afterErr: test.afterErr}
			deps := testDeps(store, discoverer, base)
			deps.Targets[test.typ] = target
			var output bytes.Buffer
			deps.Log = slog.New(slog.NewJSONHandler(&output, nil))

			summary, err := Run(context.Background(), nil, deps, time.Now())

			require.NoError(t, err)
			assert.Equal(t, []string{resource.Ref, resource.Ref}, base.Described)
			record := requireLogRecord(t, readLogRecords(t, &output), "action-finished", "resource_id", resource.ID())
			assert.Equal(t, "INFO", record["level"])
			assert.Equal(t, resource.ARN, record["resource_arn"])
			assert.Equal(t, string(test.typ), record["resource_type"])
			info, _ := model.Info(test.typ)
			assert.Equal(t, info.ARNService, record["service"])
			desired, action := "running", "start"
			if test.override == model.OverrideStopped {
				desired, action = "stopped", "stop"
			}
			assert.Equal(t, desired, record["desired"])
			assert.Equal(t, action, record["action"])
			assert.Equal(t, string(test.before.State), record["observed_before"])
			assert.Equal(t, test.before.Detail, record["detail_before"])
			assert.Equal(t, test.actionErr == nil, record["success"])
			if test.afterErr == nil {
				assert.Equal(t, string(test.after.State), record["observed_after"])
				assert.Equal(t, test.after.Detail, record["detail_after"])
				assert.NotContains(t, record, "observation_error")
			} else {
				assert.Nil(t, record["observed_after"])
				assert.Equal(t, test.afterErr.Error(), record["observation_error"])
			}
			if test.actionErr == nil {
				assert.Len(t, summary.Actions, 1)
				assert.Len(t, deps.Notifier.(*porttest.Notifier).Published, 1)
				assert.NotContains(t, record, "error")
			} else {
				assert.Empty(t, summary.Actions)
				assert.Empty(t, deps.Notifier.(*porttest.Notifier).Published)
				assert.Equal(t, test.actionErr.Error(), record["error"])
			}
			if test.actionErr != nil || test.afterErr != nil {
				assert.Len(t, summary.Errors, 1)
			} else {
				assert.Empty(t, summary.Errors)
			}
		})
	}
}

func TestInfoLogsIncludeUnchangedAndSkippedResourceStates(t *testing.T) {
	for _, test := range []struct {
		name     string
		override model.Override
		observed model.ObservedState
		message  string
	}{
		{"unchanged", model.OverrideRunning, model.StateRunning, "resource-unchanged"},
		{"transitioning", model.OverrideRunning, model.StateTransitioning, "resource-skipped"},
		{"not found", model.OverrideRunning, model.StateNotFound, "resource-skipped"},
		{"disabled", model.OverrideDisabled, "", "resource-skipped"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: test.override}}}}
			discoverer := porttest.NewDiscoverer()
			resource := taggedResource("arn:db", "db", "dev")
			discoverer.Resources[resource.ARN] = resource
			target := porttest.NewTarget(resource.Type)
			target.Observations[resource.Ref] = model.Observation{State: test.observed}
			deps := testDeps(store, discoverer, target)
			var output bytes.Buffer
			deps.Log = slog.New(slog.NewJSONHandler(&output, nil))

			summary, err := Run(context.Background(), nil, deps, time.Now())

			require.NoError(t, err)
			assert.Empty(t, summary.Actions)
			assert.Empty(t, summary.Errors)
			record := requireLogRecord(t, readLogRecords(t, &output), test.message, "resource_id", resource.ID())
			assert.Equal(t, "INFO", record["level"])
			assert.Equal(t, "rds", record["service"])
			assert.Equal(t, string(resource.Type), record["resource_type"])
			if test.override == model.OverrideDisabled {
				assert.Empty(t, target.Described)
				assert.Equal(t, "", record["desired"])
				assert.Contains(t, record, "observed_before")
				assert.Contains(t, record, "observed_after")
				assert.Nil(t, record["observed_before"])
				assert.Nil(t, record["observed_after"])
			} else {
				assert.Equal(t, []string{resource.Ref}, target.Described)
				assert.Equal(t, "running", record["desired"])
				assert.Equal(t, string(test.observed), record["observed_before"])
				assert.Equal(t, string(test.observed), record["observed_after"])
			}
		})
	}
}

func TestPostActionObservationFailureDoesNotDelayResultUntilLaterResources(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideRunning}}}}
	discoverer := porttest.NewDiscoverer()
	a := taggedResource("a", "db", "dev")
	b := model.Resource{Type: model.TypeEc2Instance, ARN: "b", Ref: "i-0123", Tags: map[string]string{model.GroupTagKey: "dev"}}
	discoverer.Resources[a.ARN], discoverer.Resources[b.ARN] = a, b
	aBase, bTarget := porttest.NewTarget(a.Type), porttest.NewTarget(b.Type)
	aBase.Observations[a.Ref] = model.Observation{State: model.StateStopped}
	bTarget.Observations[b.Ref] = model.Observation{State: model.StateStopped}
	deps := testDeps(store, discoverer, aBase, bTarget)
	deps.Targets[a.Type] = &afterActionTarget{Target: aBase, afterErr: errors.New("describe unavailable")}
	var output bytes.Buffer
	deps.Log = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))

	summary, err := Run(context.Background(), nil, deps, time.Now())

	require.NoError(t, err)
	assert.Equal(t, 2, summary.Reconciled)
	assert.Len(t, summary.Actions, 2)
	assert.Len(t, summary.Errors, 1)
	assert.Len(t, deps.Notifier.(*porttest.Notifier).Published, 2)
	aFinished, bStarted := -1, -1
	for index, record := range readLogRecords(t, &output) {
		if record["msg"] == "action-finished" && record["resource_id"] == a.ID() {
			aFinished = index
		}
		if record["msg"] == "describe-resource-started" && record["resource_id"] == b.ID() {
			bStarted = index
		}
	}
	require.NotEqual(t, -1, aFinished)
	require.NotEqual(t, -1, bStarted)
	assert.Less(t, aFinished, bStarted)
}

func readLogRecords(t *testing.T, output *bytes.Buffer) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	var records []map[string]any
	for {
		var record map[string]any
		err := decoder.Decode(&record)
		if errors.Is(err, io.EOF) {
			return records
		}
		require.NoError(t, err)
		records = append(records, record)
	}
}

func requireLogRecord(t *testing.T, records []map[string]any, message, key string, value any) map[string]any {
	t.Helper()
	for _, record := range records {
		if record["msg"] == message && record[key] == value {
			return record
		}
	}
	require.FailNow(t, "missing log record", "msg=%s %s=%v", message, key, value)
	return nil
}
