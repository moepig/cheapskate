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
	for _, step := range []string{"reconcile", "load-groups", "resolve-desired", "discover-resources", "reconcile-resources", "describe-resource", "decide-action", "apply-action", "notify-action"} {
		start := requireLogRecord(t, records, "step-started", "step", step)
		finish := requireLogRecord(t, records, "step-finished", "step", step)
		assert.Equal(t, "DEBUG", start["level"])
		assert.Equal(t, "DEBUG", finish["level"])
		assert.Equal(t, true, finish["success"])
	}
	assert.Equal(t, float64(3), requireLogRecord(t, records, "step-finished", "step", "discover-resources")["resources"])
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
			requireLogRecord(t, records, "step-started", "step", step)
			finish := requireLogRecord(t, records, "step-finished", "step", step)
			assert.Equal(t, false, finish["success"])
			assert.Equal(t, failure.Error(), finish["error"])
			assert.Equal(t, !globalFailure, requireLogRecord(t, records, "step-finished", "step", "reconcile")["success"])
			if step == "notify-action" {
				var attempts []float64
				for _, record := range records {
					if record["msg"] == "step-finished" && record["step"] == step {
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
				assert.Equal(t, string(observed), record["observed"])
			}}

			summary, err := Run(context.Background(), nil, deps, time.Now())

			require.NoError(t, err)
			assert.True(t, called)
			assert.Empty(t, summary.Actions)
			assert.Len(t, summary.Errors, 1)
			for _, record := range readLogRecords(t, &output) {
				assert.NotEqual(t, "DEBUG", record["level"])
				assert.NotEqual(t, "action", record["msg"])
			}
		})
	}
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
