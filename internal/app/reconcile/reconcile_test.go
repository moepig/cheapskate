package reconcile

import (
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

type memoryStore struct {
	rows []state.GroupRow
	err  error
}

func (store *memoryStore) ListGroups(context.Context) ([]state.GroupRow, error) {
	return store.rows, store.err
}

func testDeps(store Store, discoverer port.Discoverer, targets ...*porttest.Target) *Deps {
	targetMap := map[model.ResourceType]port.Target{}
	for _, target := range targets {
		targetMap[target.Type()] = target
	}
	return &Deps{
		Store: store, Discoverer: discoverer, Targets: targetMap,
		Notifier: &porttest.Notifier{}, Location: time.UTC,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func taggedResource(arn, ref, group string) model.Resource {
	return model.Resource{Type: model.TypeRdsInstance, ARN: arn, Ref: ref, Tags: map[string]string{model.GroupTagKey: group}}
}

func TestRunIgnoresPayloadAndReconcilesAllResources(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideStopped}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"a": taggedResource("a", "a", "dev"),
		"b": taggedResource("b", "b", "dev"),
	}
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["a"] = model.Observation{State: model.StateRunning}
	target.Observations["b"] = model.Observation{State: model.StateRunning}

	summary, err := Run(context.Background(), json.RawMessage(`not-json`), testDeps(store, discoverer, target), now)
	require.NoError(t, err)
	assert.Equal(t, 2, summary.Reconciled)
	assert.ElementsMatch(t, []string{"a", "b"}, target.Stopped)
	assert.Len(t, summary.Actions, 2)
	assert.Empty(t, summary.Errors)
}

func TestDiscoveryFailureAbortsBeforeDescribe(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideStopped}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Err = errors.New("tagging unavailable")
	target := porttest.NewTarget(model.TypeRdsInstance)

	_, err := Run(context.Background(), nil, testDeps(store, discoverer, target), time.Now())
	assert.ErrorContains(t, err, "tagging unavailable")
	assert.Empty(t, target.Described)
	assert.Empty(t, target.Stopped)
	assert.Empty(t, target.Started)
}

func TestConfigurationFailureAbortsBeforeDiscovery(t *testing.T) {
	store := &memoryStore{err: errors.New("query unavailable")}
	discoverer := porttest.NewDiscoverer()
	target := porttest.NewTarget(model.TypeRdsInstance)

	_, err := Run(context.Background(), nil, testDeps(store, discoverer, target), time.Now())

	assert.ErrorContains(t, err, "query unavailable")
	assert.Zero(t, discoverer.Calls())
	assert.Empty(t, target.Described)
}

func TestResourceErrorsDoNotStopOtherResources(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideStopped}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"bad":  taggedResource("bad", "bad", "unknown"),
		"good": taggedResource("good", "good", "dev"),
	}
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["good"] = model.Observation{State: model.StateRunning}

	summary, err := Run(context.Background(), nil, testDeps(store, discoverer, target), time.Now())
	require.NoError(t, err)
	assert.Equal(t, []string{"good"}, target.Stopped)
	assert.Len(t, summary.Actions, 1)
	assert.Len(t, summary.Errors, 1)
}

func TestActionFailureDoesNotStopLaterResourceOrNotify(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideStopped}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"a": taggedResource("a", "a", "dev"),
		"b": taggedResource("b", "b", "dev"),
	}
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["a"] = model.Observation{State: model.StateRunning}
	target.Observations["b"] = model.Observation{State: model.StateRunning}
	target.StopErrs["a"] = errors.New("cannot stop a")
	deps := testDeps(store, discoverer, target)
	notifier := deps.Notifier.(*porttest.Notifier)

	summary, err := Run(context.Background(), nil, deps, time.Now())

	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, target.Stopped)
	assert.Len(t, summary.Errors, 1)
	assert.Len(t, summary.Actions, 1)
	assert.Len(t, notifier.Published, 1)
}

func TestInvalidAndDisabledGroupsDoNotDescribe(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{
		{Name: "bad", Group: model.GroupSpec{Name: "bad"}},
		{Name: "off", Group: model.GroupSpec{Name: "off", Override: model.OverrideDisabled}},
	}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"bad": taggedResource("bad", "bad", "bad"),
		"off": taggedResource("off", "off", "off"),
	}
	target := porttest.NewTarget(model.TypeRdsInstance)

	summary, err := Run(context.Background(), nil, testDeps(store, discoverer, target), time.Now())
	require.NoError(t, err)
	assert.Zero(t, summary.Reconciled)
	assert.Len(t, summary.Errors, 2)
	assert.Empty(t, target.Described)
	assert.Empty(t, target.Stopped)
}

func TestLaterCycleConvergesToLatestSnapshot(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideStopped}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{"db": taggedResource("db", "db", "dev")}
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["db"] = model.Observation{State: model.StateRunning}
	deps := testDeps(store, discoverer, target)

	_, err := Run(context.Background(), nil, deps, time.Now())
	require.NoError(t, err)
	store.rows[0].Group.Override = model.OverrideRunning
	target.Observations["db"] = model.Observation{State: model.StateStopped}
	_, err = Run(context.Background(), nil, deps, time.Now())
	require.NoError(t, err)
	assert.Equal(t, []string{"db"}, target.Stopped)
	assert.Equal(t, []string{"db"}, target.Started)
}

func TestRunningResourceWithStartRepairIsStarted(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideRunning}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{"service": taggedResource("service", "service", "dev")}
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["service"] = model.Observation{State: model.StateRunning, NeedsStart: true}

	summary, err := Run(context.Background(), nil, testDeps(store, discoverer, target), time.Now())

	require.NoError(t, err)
	assert.Equal(t, []string{"service"}, target.Started)
	require.Len(t, summary.Actions, 1)
	assert.Equal(t, model.ActionStart, summary.Actions[0].Action)
}

func TestStoppedResourceWithStopRepairIsStopped(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideStopped}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{"service": taggedResource("service", "service", "dev")}
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["service"] = model.Observation{State: model.StateStopped, NeedsStop: true}

	summary, err := Run(context.Background(), nil, testDeps(store, discoverer, target), time.Now())

	require.NoError(t, err)
	assert.Equal(t, []string{"service"}, target.Stopped)
	require.Len(t, summary.Actions, 1)
	assert.Equal(t, model.ActionStop, summary.Actions[0].Action)
}

func TestConvergedTransitioningAndNotFoundResourcesCauseNoActionOrNotification(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideRunning}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"running":       taggedResource("running", "running", "dev"),
		"transitioning": taggedResource("transitioning", "transitioning", "dev"),
		"missing":       taggedResource("missing", "missing", "dev"),
	}
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["running"] = model.Observation{State: model.StateRunning}
	target.Observations["transitioning"] = model.Observation{State: model.StateTransitioning}
	target.Observations["missing"] = model.Observation{State: model.StateNotFound}
	deps := testDeps(store, discoverer, target)
	notifier := deps.Notifier.(*porttest.Notifier)

	summary, err := Run(context.Background(), nil, deps, time.Now())

	require.NoError(t, err)
	assert.Equal(t, 3, summary.Reconciled)
	assert.Empty(t, summary.Actions)
	assert.Empty(t, summary.Errors)
	assert.Empty(t, target.Started)
	assert.Empty(t, target.Stopped)
	assert.Empty(t, notifier.Published)
}

func TestEmptyAndInvalidMembershipTagsAreRejectedBeforeAnyOperation(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideStopped}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{
		"missing": {Type: model.TypeRdsInstance, ARN: "missing", Ref: "missing", Tags: map[string]string{}},
		"empty":   {Type: model.TypeRdsInstance, ARN: "empty", Ref: "empty", Tags: map[string]string{model.GroupTagKey: ""}},
		"invalid": {Type: model.TypeRdsInstance, ARN: "invalid", Ref: "invalid", Tags: map[string]string{model.GroupTagKey: "bad/name"}},
	}
	target := porttest.NewTarget(model.TypeRdsInstance)
	deps := testDeps(store, discoverer, target)
	notifier := deps.Notifier.(*porttest.Notifier)

	summary, err := Run(context.Background(), nil, deps, time.Now())

	require.NoError(t, err)
	assert.Zero(t, summary.Reconciled)
	assert.Len(t, summary.Errors, 3)
	assert.Empty(t, target.Described)
	assert.Empty(t, target.Started)
	assert.Empty(t, target.Stopped)
	assert.Empty(t, notifier.Published)
}

func TestSuccessfulActionNotificationHasSlimPayload(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideRunning}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{"db": taggedResource("db", "db", "dev")}
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["db"] = model.Observation{State: model.StateStopped}
	deps := testDeps(store, discoverer, target)
	notifier := deps.Notifier.(*porttest.Notifier)

	_, err := Run(context.Background(), nil, deps, time.Unix(100, 0))
	require.NoError(t, err)
	require.Len(t, notifier.Published, 1)
	payload := notifier.Published[0].Payload
	assert.ElementsMatch(t, []string{"group", "resource_id", "action", "desired", "at"}, mapKeys(payload))
}

func TestActionNotificationIsAttemptedAtMostTwice(t *testing.T) {
	store := &memoryStore{rows: []state.GroupRow{{Name: "dev", Group: model.GroupSpec{Name: "dev", Override: model.OverrideRunning}}}}
	discoverer := porttest.NewDiscoverer()
	discoverer.Resources = map[string]model.Resource{"db": taggedResource("db", "db", "dev")}
	target := porttest.NewTarget(model.TypeRdsInstance)
	target.Observations["db"] = model.Observation{State: model.StateStopped}
	deps := testDeps(store, discoverer, target)
	notifier := deps.Notifier.(*porttest.Notifier)
	notifier.Err = errors.New("publish failed")

	summary, err := Run(context.Background(), nil, deps, time.Now())

	require.NoError(t, err)
	assert.Len(t, summary.Actions, 1)
	assert.Empty(t, summary.Errors)
	assert.Len(t, notifier.Published, 2)
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
