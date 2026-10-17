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
		assert.Equal(t, resource.Tags[model.GroupTagKey], record["group"])
	}
	assert.Equal(t, []string{"a", "b", "c"}, discoveredARNs)
	for _, step := range []string{"reconcile", "load-groups", "resolve-desired", "discover-resources", "reconcile-resources", "describe-resource", "decide-action", "apply-action", "notify-action"} {
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
		assert.NotContains(t, record, "service")
		assert.NotContains(t, record, "resource_type")
		assert.NotEqual(t, "describe-resource-after-action-started", record["msg"])
		assert.NotEqual(t, "describe-resource-after-action-finished", record["msg"])
	}
}

func TestFailedStepsAreFinishedWithTheError(t *testing.T) {
	for _, step := range []string{"load-groups", "discover-resources", "describe-resource", "apply-action", "notify-action"} {
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
				assert.NotContains(t, record, "resource_type")
				assert.NotContains(t, record, "service")
				assert.Equal(t, string(observed), record["observed"])
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

func TestInfoLogsRecordActionResultsWithoutObservingAgain(t *testing.T) {
	for _, test := range []struct {
		name      string
		typ       model.ResourceType
		override  model.Override
		before    model.Observation
		actionErr error
	}{
		{name: "RDS start", typ: model.TypeRdsInstance, override: model.OverrideRunning, before: model.Observation{State: model.StateStopped, Detail: "stopped"}},
		{name: "Aurora stop", typ: model.TypeRdsCluster, override: model.OverrideStopped, before: model.Observation{State: model.StateRunning, Detail: "available"}},
		{name: "EC2 stop", typ: model.TypeEc2Instance, override: model.OverrideStopped, before: model.Observation{State: model.StateRunning, Detail: "running"}},
		{name: "ECS repair", typ: model.TypeEcsService, override: model.OverrideRunning, before: model.Observation{State: model.StateRunning, Detail: "ScheduledScalingSuspended=true", NeedsStart: true}},
		{name: "start failure", typ: model.TypeEcsService, override: model.OverrideRunning, before: model.Observation{State: model.StateStopped}, actionErr: errors.New("response lost")},
		{name: "stop failure", typ: model.TypeEc2Instance, override: model.OverrideStopped, before: model.Observation{State: model.StateRunning}, actionErr: errors.New("cannot stop")},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: test.override}}}}
			discoverer := porttest.NewDiscoverer()
			resource := model.Resource{Type: test.typ, ARN: "arn:resource", Ref: "resource", Tags: map[string]string{model.GroupTagKey: "dev"}}
			discoverer.Resources[resource.ARN] = resource
			target := porttest.NewTarget(test.typ)
			target.Observations[resource.Ref] = test.before
			target.StartErr, target.StopErr = test.actionErr, test.actionErr
			deps := testDeps(store, discoverer, target)
			var output bytes.Buffer
			deps.Log = slog.New(slog.NewJSONHandler(&output, nil))

			summary, err := Run(context.Background(), nil, deps, time.Now())

			require.NoError(t, err)
			assert.Equal(t, []string{resource.Ref}, target.Described)
			record := requireLogRecord(t, readLogRecords(t, &output), "action-finished", "resource_id", resource.ID())
			assert.Equal(t, "INFO", record["level"])
			assert.Equal(t, resource.ARN, record["resource_arn"])
			assert.NotContains(t, record, "resource_type")
			assert.NotContains(t, record, "service")
			desired, action := "running", "start"
			if test.override == model.OverrideStopped {
				desired, action = "stopped", "stop"
			}
			assert.Equal(t, desired, record["desired"])
			assert.Equal(t, action, record["action"])
			assert.Equal(t, string(test.before.State), record["observed"])
			assert.Equal(t, test.before.Detail, record["aws_state"])
			assert.Equal(t, test.actionErr == nil, record["success"])
			for _, record := range readLogRecords(t, &output) {
				assert.NotContains(t, record, "observed_before")
				assert.NotContains(t, record, "observed_after")
				assert.NotContains(t, record, "detail_before")
				assert.NotContains(t, record, "detail_after")
				assert.NotContains(t, record, "observation_error")
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
			if test.actionErr != nil {
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
		reason   string
	}{
		{"already running", model.OverrideRunning, model.StateRunning, "resource-unchanged", "desired-state-matched"},
		{"already stopped", model.OverrideStopped, model.StateStopped, "resource-unchanged", "desired-state-matched"},
		{"transitioning", model.OverrideRunning, model.StateTransitioning, "resource-skipped", "transitioning"},
		{"not found", model.OverrideRunning, model.StateNotFound, "resource-skipped", "not-found"},
		{"disabled", model.OverrideDisabled, "", "resource-skipped", "disabled"},
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
			assert.Equal(t, resource.ARN, record["resource_arn"])
			assert.Equal(t, test.reason, record["reason"])
			assert.NotContains(t, record, "service")
			assert.NotContains(t, record, "resource_type")
			assert.NotContains(t, record, "observed_before")
			assert.NotContains(t, record, "observed_after")
			if test.override == model.OverrideDisabled {
				assert.Empty(t, target.Described)
				assert.Equal(t, "", record["desired"])
				assert.Contains(t, record, "observed")
				assert.Nil(t, record["observed"])
			} else {
				assert.Equal(t, []string{resource.Ref}, target.Described)
				assert.Equal(t, string(test.override), record["desired"])
				assert.Equal(t, string(test.observed), record["observed"])
			}
		})
	}
}

func TestActionResultsAreLoggedBeforeLaterResources(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideRunning}}}}
	discoverer := porttest.NewDiscoverer()
	a := taggedResource("a", "db", "dev")
	b := model.Resource{Type: model.TypeEc2Instance, ARN: "b", Ref: "i-0123", Tags: map[string]string{model.GroupTagKey: "dev"}}
	discoverer.Resources[a.ARN], discoverer.Resources[b.ARN] = a, b
	aBase, bTarget := porttest.NewTarget(a.Type), porttest.NewTarget(b.Type)
	aBase.Observations[a.Ref] = model.Observation{State: model.StateStopped}
	bTarget.Observations[b.Ref] = model.Observation{State: model.StateStopped}
	deps := testDeps(store, discoverer, aBase, bTarget)
	var output bytes.Buffer
	deps.Log = slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))

	summary, err := Run(context.Background(), nil, deps, time.Now())

	require.NoError(t, err)
	assert.Equal(t, 2, summary.Reconciled)
	assert.Len(t, summary.Actions, 2)
	assert.Empty(t, summary.Errors)
	assert.Equal(t, []string{a.Ref}, aBase.Described)
	assert.Equal(t, []string{b.Ref}, bTarget.Described)
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
