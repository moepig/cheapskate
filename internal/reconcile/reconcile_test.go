package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"cheapskate/internal/dynafake"
	"cheapskate/internal/model"
	"cheapskate/internal/store"
	"cheapskate/internal/target"
)

// fakeTarget serves canned observations and records stop/start calls.
type fakeTarget struct {
	typ          string
	observations map[string]model.Observation
	describeErr  error
	stopErr      error
	stopped      []string
	started      []string
}

func (f *fakeTarget) Type() string { return f.typ }

func (f *fakeTarget) Describe(_ context.Context, ref string) (model.Observation, error) {
	if f.describeErr != nil {
		return model.Observation{}, f.describeErr
	}
	obs, ok := f.observations[ref]
	if !ok {
		return model.Observation{State: model.StateNotFound}, nil
	}
	return obs, nil
}

func (f *fakeTarget) PrepareStop(_ context.Context, ref string, _ model.Config, _ model.Status) (*model.SavedState, error) {
	count := int32(3)
	return &model.SavedState{DesiredCount: &count}, nil
}

func (f *fakeTarget) Stop(_ context.Context, ref string, _ model.Config, _ model.Status) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	f.stopped = append(f.stopped, ref)
	return nil
}

func (f *fakeTarget) Start(_ context.Context, ref string, _ model.Config, _ model.Status) (*model.SavedState, error) {
	f.started = append(f.started, ref)
	return nil, nil
}

type notification struct {
	subject string
	payload map[string]any
}

type fakeNotifier struct {
	published  []notification
	publishErr error
}

func (f *fakeNotifier) Publish(_ context.Context, subject string, payload map[string]any) error {
	f.published = append(f.published, notification{subject, payload})
	return f.publishErr
}

type fixture struct {
	db       *dynafake.Fake
	deps     *Deps
	rds      *fakeTarget
	cluster  *fakeTarget
	ecs      *fakeTarget
	notifier *fakeNotifier
}

func newFixture() *fixture {
	db := dynafake.New()
	rds := &fakeTarget{typ: model.TypeRdsInstance, observations: map[string]model.Observation{}}
	cluster := &fakeTarget{typ: model.TypeRdsCluster, observations: map[string]model.Observation{}}
	ecs := &fakeTarget{typ: model.TypeEcsService, observations: map[string]model.Observation{}}
	notifier := &fakeNotifier{}
	return &fixture{
		db: db, rds: rds, cluster: cluster, ecs: ecs, notifier: notifier,
		deps: &Deps{
			Store:           store.New(db, "state"),
			Targets:         map[string]target.Target{rds.typ: rds, cluster.typ: cluster, ecs.typ: ecs},
			Notifier:        notifier,
			DefaultTimezone: "UTC",
			Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
}

func (f *fixture) seedConfig(attrs map[string]types.AttributeValue) {
	f.db.Seed(attrs)
}

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
func n(v int) types.AttributeValue    { return &types.AttributeValueMemberN{Value: fmt.Sprint(v)} }

func pinnedStopped(resourceID, typ string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": s(model.ConfigPrefix + resourceID), "type": s(typ),
		"mode": s(model.ModePinned), "desired": s(model.DesiredStopped),
	}
}

var now = time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)

func runEmpty(t *testing.T, f *fixture) Summary {
	t.Helper()
	summary, err := Run(context.Background(), json.RawMessage(`{}`), f.deps, now)
	if err != nil {
		t.Fatal(err)
	}
	return summary
}

func TestStopsRunningPinnedResource(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#dev-db", model.TypeRdsInstance))
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	if len(f.rds.stopped) != 1 || f.rds.stopped[0] != "dev-db" {
		t.Fatalf("stop calls: %v", f.rds.stopped)
	}
	if len(summary.Actions) != 1 || summary.Actions[0].Action != "stop" {
		t.Fatalf("actions: %+v", summary.Actions)
	}
	status := f.db.Item("status#rds-instance#dev-db")
	if status == nil {
		t.Fatal("status item not written")
	}
	if got := status["last_action"].(*types.AttributeValueMemberS).Value; got != "stop" {
		t.Fatalf("last_action: %q", got)
	}
	if len(f.notifier.published) != 1 {
		t.Fatalf("notifications: %d", len(f.notifier.published))
	}
	if f.notifier.published[0].subject != "[cheapskate] stop: rds-instance#dev-db" {
		t.Fatalf("subject: %q", f.notifier.published[0].subject)
	}
}

func TestConvergedWritesAndNotifiesNothing(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#dev-db", model.TypeRdsInstance))
	f.rds.observations["dev-db"] = model.Observation{State: model.StateStopped}

	summary := runEmpty(t, f)

	if len(summary.Actions) != 0 || len(summary.Errors) != 0 {
		t.Fatalf("summary: %+v", summary)
	}
	if f.db.Item("status#rds-instance#dev-db") != nil {
		t.Fatal("converged cycle must not write status")
	}
	if len(f.notifier.published) != 0 {
		t.Fatal("converged cycle must not notify")
	}
}

func TestTransitioningIsSkipped(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#dev-db", model.TypeRdsInstance))
	f.rds.observations["dev-db"] = model.Observation{State: model.StateTransitioning, Detail: "stopping"}

	summary := runEmpty(t, f)

	if len(f.rds.stopped)+len(f.rds.started) != 0 {
		t.Fatal("transitioning resource must not be acted on")
	}
	if len(summary.Actions) != 0 || len(summary.Errors) != 0 {
		t.Fatalf("summary: %+v", summary)
	}
}

func TestDisabledIsSkipped(t *testing.T) {
	f := newFixture()
	f.seedConfig(map[string]types.AttributeValue{
		"pk": s("config#rds-instance#dev-db"), "type": s(model.TypeRdsInstance), "mode": s(model.ModeDisabled),
	})
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)

	if len(f.rds.stopped) != 0 {
		t.Fatal("disabled resource must not be acted on")
	}
}

func TestNotFoundRecordsError(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#gone", model.TypeRdsInstance))

	summary := runEmpty(t, f)

	if len(summary.Errors) != 1 {
		t.Fatalf("errors: %+v", summary.Errors)
	}
	status := f.db.Item("status#rds-instance#gone")
	if status == nil || status["last_error"] == nil {
		t.Fatal("last_error not recorded")
	}
	if len(f.notifier.published) != 1 {
		t.Fatalf("error must notify once, got %d", len(f.notifier.published))
	}
}

// B-3: an unchanging error (e.g. a resource that stays deleted) must notify once, not every cycle.
func TestRepeatedSameErrorNotifiesOnce(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#gone", model.TypeRdsInstance))

	runEmpty(t, f)
	runEmpty(t, f)

	if len(f.notifier.published) != 1 {
		t.Fatalf("repeated identical error must notify once, got %d: %+v", len(f.notifier.published), f.notifier.published)
	}
}

// B-3: a change in the error message (a new failure mode) must notify again.
func TestChangedErrorNotifiesAgain(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#dev-db", model.TypeRdsInstance))
	f.rds.describeErr = fmt.Errorf("first failure")

	runEmpty(t, f)

	f.rds.describeErr = fmt.Errorf("second, different failure")
	runEmpty(t, f)

	if len(f.notifier.published) != 2 {
		t.Fatalf("changed error must notify again, got %d: %+v", len(f.notifier.published), f.notifier.published)
	}
}

// B-11: once an erroring resource converges, a single "recovered" notification fires and last_error is cleared.
func TestRecoveryNotifiesOnceAndClearsError(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#dev-db", model.TypeRdsInstance))
	f.rds.describeErr = fmt.Errorf("transient failure")

	runEmpty(t, f)
	if len(f.notifier.published) != 1 {
		t.Fatalf("initial error must notify: %+v", f.notifier.published)
	}

	f.rds.describeErr = nil
	f.rds.observations["dev-db"] = model.Observation{State: model.StateStopped} // already converged: desired=stopped
	runEmpty(t, f)

	if len(f.notifier.published) != 2 {
		t.Fatalf("recovery must notify once, got %d: %+v", len(f.notifier.published), f.notifier.published)
	}
	if f.notifier.published[1].subject != "[cheapskate] recovered: rds-instance#dev-db" {
		t.Fatalf("recovery subject: %q", f.notifier.published[1].subject)
	}
	status := f.db.Item("status#rds-instance#dev-db")
	if status["last_error"].(*types.AttributeValueMemberS).Value != "" {
		t.Fatalf("last_error must be cleared: %v", status["last_error"])
	}

	runEmpty(t, f)
	if len(f.notifier.published) != 2 {
		t.Fatalf("already-recovered convergence must not notify again, got %d", len(f.notifier.published))
	}
}

// A-7: a Publish failure after a successful action must not be recorded as a reconcile error (B-4) — the action succeeded and is already persisted.
func TestNotifyFailureAfterSuccessfulActionIsNotAnError(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#dev-db", model.TypeRdsInstance))
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}
	f.notifier.publishErr = fmt.Errorf("sns down")

	summary := runEmpty(t, f)

	if len(summary.Errors) != 0 {
		t.Fatalf("notify failure must not surface as a reconcile error: %+v", summary.Errors)
	}
	if len(summary.Actions) != 1 {
		t.Fatalf("action must still be recorded: %+v", summary.Actions)
	}
	status := f.db.Item("status#rds-instance#dev-db")
	if status == nil || status["last_error"] != nil {
		t.Fatalf("notify failure must not be written as last_error: %v", status)
	}
}

// A-7: a PutStatus failure after a successful action is recorded as the cycle's error, and other resources are still reconciled (per-resource isolation holds even when persistence itself is what fails).
func TestPutStatusFailureAfterActionIsRecordedButIsolated(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#dev-db", model.TypeRdsInstance))
	f.seedConfig(pinnedStopped("rds-cluster#b-fine", model.TypeRdsCluster))
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}
	f.cluster.observations["b-fine"] = model.Observation{State: model.StateRunning}
	f.db.FailOn("update", "status#rds-instance#dev-db", fmt.Errorf("dynamodb unavailable"))

	summary := runEmpty(t, f)

	if len(summary.Errors) != 1 || summary.Errors[0].ResourceID != "rds-instance#dev-db" {
		t.Fatalf("errors: %+v", summary.Errors)
	}
	if len(f.cluster.stopped) != 1 {
		t.Fatal("second resource must still be reconciled")
	}
}

// A-7: when even the error-recording PutStatus fails, Run must not panic and must still process other resources; the failure is only logged.
func TestErrorRecordingFailureDoesNotPanic(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#a-broken", model.TypeRdsInstance))
	f.seedConfig(pinnedStopped("rds-cluster#b-fine", model.TypeRdsCluster))
	f.rds.describeErr = fmt.Errorf("boom")
	f.cluster.observations["b-fine"] = model.Observation{State: model.StateRunning}
	f.db.FailOn("update", "status#rds-instance#a-broken", fmt.Errorf("dynamodb also down"))
	f.notifier.publishErr = fmt.Errorf("sns also down")

	summary := runEmpty(t, f)

	if len(summary.Errors) != 1 || summary.Errors[0].ResourceID != "rds-instance#a-broken" {
		t.Fatalf("errors: %+v", summary.Errors)
	}
	if len(f.cluster.stopped) != 1 {
		t.Fatal("second resource must still be reconciled despite the first's reporting failing entirely")
	}
}

func TestOneFailureDoesNotBreakOthers(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#a-broken", model.TypeRdsInstance))
	f.seedConfig(pinnedStopped("rds-cluster#b-fine", model.TypeRdsCluster))
	f.rds.describeErr = fmt.Errorf("boom")
	f.cluster.observations["b-fine"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	if len(summary.Errors) != 1 || summary.Errors[0].ResourceID != "rds-instance#a-broken" {
		t.Fatalf("errors: %+v", summary.Errors)
	}
	if len(f.cluster.stopped) != 1 {
		t.Fatal("second resource must still be reconciled")
	}
}

func TestOverrideBeatsPinnedConfig(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-instance#dev-db", model.TypeRdsInstance))
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("override#rds-instance#dev-db"), "desired": s(model.DesiredRunning),
		"expires_at": n(int(now.Add(time.Hour).Unix())),
	})
	f.rds.observations["dev-db"] = model.Observation{State: model.StateStopped}

	runEmpty(t, f)

	if len(f.rds.started) != 1 {
		t.Fatal("override running must start the stopped instance")
	}
}

func TestStatusAttrsFromTargetArePersisted(t *testing.T) {
	f := newFixture()
	f.seedConfig(map[string]types.AttributeValue{
		"pk": s("config#ecs#dev/api"), "type": s(model.TypeEcsService),
		"mode": s(model.ModePinned), "desired": s(model.DesiredStopped),
	})
	f.ecs.observations["dev/api"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)

	status := f.db.Item("status#ecs#dev/api")
	if status == nil {
		t.Fatal("status item not written")
	}
	if got := status["saved_desired_count"].(*types.AttributeValueMemberN).Value; got != "3" {
		t.Fatalf("saved_desired_count: %q", got)
	}
}

// B-1: PrepareStop's saved state must be persisted before the mutating Stop call, so a crash (simulated here as Stop failing outright) still leaves a restorable saved_desired_count instead of none at all.
func TestSavedStateIsPersistedBeforeMutatingStopFails(t *testing.T) {
	f := newFixture()
	f.seedConfig(map[string]types.AttributeValue{
		"pk": s("config#ecs#dev/api"), "type": s(model.TypeEcsService),
		"mode": s(model.ModePinned), "desired": s(model.DesiredStopped),
	})
	f.ecs.observations["dev/api"] = model.Observation{State: model.StateRunning}
	f.ecs.stopErr = fmt.Errorf("boom: crashed mid-mutation")

	summary := runEmpty(t, f)

	if len(summary.Errors) != 1 {
		t.Fatalf("errors: %+v", summary.Errors)
	}
	if len(f.ecs.stopped) != 0 {
		t.Fatal("Stop must have failed, not succeeded")
	}
	status := f.db.Item("status#ecs#dev/api")
	if status == nil {
		t.Fatal("saved state must be written even though Stop failed")
	}
	if got := status["saved_desired_count"].(*types.AttributeValueMemberN).Value; got != "3" {
		t.Fatalf("saved_desired_count: %q", got)
	}
}

func loadFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRdsEventReconcilesOnlyThatResource(t *testing.T) {
	f := newFixture()
	f.seedConfig(pinnedStopped("rds-cluster#dev-aurora", model.TypeRdsCluster))
	f.seedConfig(pinnedStopped("rds-instance#other-db", model.TypeRdsInstance))
	f.cluster.observations["dev-aurora"] = model.Observation{State: model.StateRunning}
	f.rds.observations["other-db"] = model.Observation{State: model.StateRunning}

	summary, err := Run(context.Background(), loadFixture(t, "rds-event-0153-cluster-autostart.json"), f.deps, now)
	if err != nil {
		t.Fatal(err)
	}

	if summary.Reconciled != 1 {
		t.Fatalf("reconciled: %d", summary.Reconciled)
	}
	if len(f.cluster.stopped) != 1 {
		t.Fatal("event resource must be stopped")
	}
	if len(f.rds.stopped) != 0 {
		t.Fatal("other resources must not be touched on an event")
	}
}

func TestRdsEventForUnregisteredResourceIsIgnored(t *testing.T) {
	f := newFixture()
	summary, err := Run(context.Background(), loadFixture(t, "rds-event-0088-instance-started.json"), f.deps, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Reconciled != 0 || len(summary.Errors) != 0 {
		t.Fatalf("summary: %+v", summary)
	}
}

// C-4: an event with a source we don't recognize (neither "" nor "aws.rds") must still fall
// back to a full reconcile, but log a warning so an operator notices an unexpected trigger.
func TestUnexpectedEventSourceWarnsAndFallsBackToFullReconcile(t *testing.T) {
	f := newFixture()
	var logBuf bytes.Buffer
	f.deps.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	f.seedConfig(pinnedStopped("rds-instance#db1", model.TypeRdsInstance))
	f.rds.observations["db1"] = model.Observation{State: model.StateRunning}

	summary, err := Run(context.Background(), json.RawMessage(`{"source":"aws.partner/whatever"}`), f.deps, now)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Reconciled != 1 {
		t.Fatalf("expected fallback to full reconcile, got: %+v", summary)
	}
	if !strings.Contains(logBuf.String(), "unexpected-event-source") {
		t.Errorf("expected unexpected-event-source warning in logs, got:\n%s", logBuf.String())
	}
}

func TestRdsEventResourceID(t *testing.T) {
	var event Event
	if err := json.Unmarshal(loadFixture(t, "rds-event-0088-instance-started.json"), &event); err != nil {
		t.Fatal(err)
	}
	if got := RdsEventResourceID(event); got != "rds-instance#dev-db" {
		t.Fatalf("got %q", got)
	}
	if got := RdsEventResourceID(Event{Source: "aws.rds"}); got != "" {
		t.Fatalf("malformed event must map to empty, got %q", got)
	}
}
