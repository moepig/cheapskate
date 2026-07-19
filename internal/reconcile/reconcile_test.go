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
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/mocks"
	"cheapskate/internal/model"
	"cheapskate/internal/store"
	"cheapskate/internal/target"
)

// targetDouble wraps a generated MockTarget with canned observations and recorded stop/start
// calls — a stateful double is easier to follow than per-test EXPECTs here since most tests make
// several calls across a run and mutate the double's state (describeErr, stopErr) mid-test.
type targetDouble struct {
	*mocks.MockTarget
	typ          string
	observations map[string]model.Observation
	describeErr  error
	stopErr      error
	stopped      []string
	started      []string
}

func newTargetDouble(ctrl *gomock.Controller, typ string) *targetDouble {
	d := &targetDouble{MockTarget: mocks.NewMockTarget(ctrl), typ: typ, observations: map[string]model.Observation{}}
	d.EXPECT().Type().Return(typ).AnyTimes()
	d.EXPECT().Describe(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(_ context.Context, ref string) (model.Observation, error) {
			if d.describeErr != nil {
				return model.Observation{}, d.describeErr
			}
			if obs, ok := d.observations[ref]; ok {
				return obs, nil
			}
			return model.Observation{State: model.StateNotFound}, nil
		})
	d.EXPECT().PrepareStop(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(context.Context, string, model.Member, model.Status) (*model.SavedState, error) {
			count := int32(3)
			return &model.SavedState{DesiredCount: &count}, nil
		})
	d.EXPECT().Stop(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(_ context.Context, ref string, _ model.Member, _ model.Status) error {
			if d.stopErr != nil {
				return d.stopErr
			}
			d.stopped = append(d.stopped, ref)
			return nil
		})
	d.EXPECT().Start(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(_ context.Context, ref string, _ model.Member, _ model.Status) (*model.SavedState, error) {
			d.started = append(d.started, ref)
			return nil, nil
		})
	return d
}

type notification struct {
	subject string
	payload map[string]any
}

type notifierDouble struct {
	*mocks.MockNotifier
	published  []notification
	publishErr error
}

func newNotifierDouble(ctrl *gomock.Controller) *notifierDouble {
	d := &notifierDouble{MockNotifier: mocks.NewMockNotifier(ctrl)}
	d.EXPECT().Publish(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
		func(_ context.Context, subject string, payload map[string]any) error {
			d.published = append(d.published, notification{subject, payload})
			return d.publishErr
		})
	return d
}

type fixture struct {
	db       *mocks.DynaStore
	deps     *Deps
	rds      *targetDouble
	cluster  *targetDouble
	ecs      *targetDouble
	notifier *notifierDouble
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	rds := newTargetDouble(ctrl, model.TypeRdsInstance)
	cluster := newTargetDouble(ctrl, model.TypeRdsCluster)
	ecs := newTargetDouble(ctrl, model.TypeEcsService)
	notifier := newNotifierDouble(ctrl)
	return &fixture{
		db: db, rds: rds, cluster: cluster, ecs: ecs, notifier: notifier,
		deps: &Deps{
			Store:           store.New(api, "state"),
			Targets:         map[string]target.Target{rds.typ: rds, cluster.typ: cluster, ecs.typ: ecs},
			Notifier:        notifier,
			DefaultTimezone: "UTC",
			Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
}

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
func n(v int) types.AttributeValue    { return &types.AttributeValueMemberN{Value: fmt.Sprint(v)} }

func (f *fixture) seedTag(name, mode, desired string) {
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s(model.TagPrefix + name), "mode": s(mode), "desired": s(desired),
	})
}

func (f *fixture) seedMember(tag, resourceID, typ string) {
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s(model.MemberPrefix + resourceID), "tag": s(tag), "type": s(typ),
	})
}

// pinnedStoppedMember seeds a single-member pinned/stopped tag, named after the member's
// identifier — most tests here exercise exactly one resource, so the tag name doubles as a
// human-readable handle for it.
func (f *fixture) pinnedStoppedMember(tag, resourceID, typ string) {
	f.seedTag(tag, model.ModePinned, model.DesiredStopped)
	f.seedMember(tag, resourceID, typ)
}

var now = time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)

func runEmpty(t *testing.T, f *fixture) Summary {
	t.Helper()
	summary, err := Run(context.Background(), json.RawMessage(`{}`), f.deps, now)
	require.NoError(t, err)
	return summary
}

func TestStopsRunningPinnedMember(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-db", "rds-instance#dev-db", model.TypeRdsInstance)
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	assert.Equal(t, []string{"dev-db"}, f.rds.stopped)
	require.Len(t, summary.Actions, 1)
	assert.Equal(t, "stop", summary.Actions[0].Action)
	assert.Equal(t, "dev-db", summary.Actions[0].Tag)
	status := f.db.Item("status#rds-instance#dev-db")
	require.NotNil(t, status, "status item not written")
	assert.Equal(t, "stop", status["last_action"].(*types.AttributeValueMemberS).Value)
	require.Len(t, f.notifier.published, 1)
	assert.Equal(t, "[cheapskate] stop: dev-db/rds-instance#dev-db", f.notifier.published[0].subject)
}

func TestStopsAllMembersOfPinnedTag(t *testing.T) {
	f := newFixture(t)
	f.seedTag("dev", model.ModePinned, model.DesiredStopped)
	f.seedMember("dev", "rds-instance#dev-db", model.TypeRdsInstance)
	f.seedMember("dev", "ecs#dev-cluster/api", model.TypeEcsService)
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}
	f.ecs.observations["dev-cluster/api"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	assert.Equal(t, []string{"dev-db"}, f.rds.stopped)
	assert.Equal(t, []string{"dev-cluster/api"}, f.ecs.stopped)
	require.Len(t, summary.Actions, 2)
	for _, a := range summary.Actions {
		assert.Equal(t, "dev", a.Tag)
	}
	require.NotNil(t, f.db.Item("status#rds-instance#dev-db"), "status stays per-resource")
	require.NotNil(t, f.db.Item("status#ecs#dev-cluster/api"), "status stays per-resource")
	assert.Len(t, f.notifier.published, 2)
}

func TestConvergedWritesAndNotifiesNothing(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-db", "rds-instance#dev-db", model.TypeRdsInstance)
	f.rds.observations["dev-db"] = model.Observation{State: model.StateStopped}

	summary := runEmpty(t, f)

	assert.Empty(t, summary.Actions)
	assert.Empty(t, summary.Errors)
	assert.Nil(t, f.db.Item("status#rds-instance#dev-db"), "converged cycle must not write status")
	assert.Empty(t, f.notifier.published, "converged cycle must not notify")
}

func TestTransitioningIsSkipped(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-db", "rds-instance#dev-db", model.TypeRdsInstance)
	f.rds.observations["dev-db"] = model.Observation{State: model.StateTransitioning, Detail: "stopping"}

	summary := runEmpty(t, f)

	assert.Empty(t, f.rds.stopped)
	assert.Empty(t, f.rds.started)
	assert.Empty(t, summary.Actions)
	assert.Empty(t, summary.Errors)
}

func TestDisabledTagSkipsAllMembers(t *testing.T) {
	f := newFixture(t)
	f.seedTag("dev", model.ModeDisabled, "")
	f.seedMember("dev", "rds-instance#dev-db", model.TypeRdsInstance)
	f.seedMember("dev", "ecs#dev-cluster/api", model.TypeEcsService)
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}
	f.ecs.observations["dev-cluster/api"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)

	assert.Empty(t, f.rds.stopped)
	assert.Empty(t, f.ecs.stopped)
}

func TestNotFoundRecordsError(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("gone", "rds-instance#gone", model.TypeRdsInstance)

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	status := f.db.Item("status#rds-instance#gone")
	require.NotNil(t, status)
	assert.NotNil(t, status["last_error"], "last_error not recorded")
	assert.Len(t, f.notifier.published, 1, "error must notify once")
}

// B-3: an unchanging error (e.g. a resource that stays deleted) must notify once, not every cycle.
func TestRepeatedSameErrorNotifiesOnce(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("gone", "rds-instance#gone", model.TypeRdsInstance)

	runEmpty(t, f)
	runEmpty(t, f)

	assert.Len(t, f.notifier.published, 1, "repeated identical error must notify once")
}

// B-3: a change in the error message (a new failure mode) must notify again.
func TestChangedErrorNotifiesAgain(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-db", "rds-instance#dev-db", model.TypeRdsInstance)
	f.rds.describeErr = fmt.Errorf("first failure")

	runEmpty(t, f)

	f.rds.describeErr = fmt.Errorf("second, different failure")
	runEmpty(t, f)

	assert.Len(t, f.notifier.published, 2, "changed error must notify again")
}

// B-11: once an erroring resource converges, a single "recovered" notification fires and last_error is cleared.
func TestRecoveryNotifiesOnceAndClearsError(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-db", "rds-instance#dev-db", model.TypeRdsInstance)
	f.rds.describeErr = fmt.Errorf("transient failure")

	runEmpty(t, f)
	require.Len(t, f.notifier.published, 1, "initial error must notify")

	f.rds.describeErr = nil
	f.rds.observations["dev-db"] = model.Observation{State: model.StateStopped} // already converged: desired=stopped
	runEmpty(t, f)

	require.Len(t, f.notifier.published, 2, "recovery must notify once")
	assert.Equal(t, "[cheapskate] recovered: dev-db/rds-instance#dev-db", f.notifier.published[1].subject)
	status := f.db.Item("status#rds-instance#dev-db")
	assert.Equal(t, "", status["last_error"].(*types.AttributeValueMemberS).Value, "last_error must be cleared")

	runEmpty(t, f)
	assert.Len(t, f.notifier.published, 2, "already-recovered convergence must not notify again")
}

// A-7: a Publish failure after a successful action must not be recorded as a reconcile error (B-4) — the action succeeded and is already persisted.
func TestNotifyFailureAfterSuccessfulActionIsNotAnError(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-db", "rds-instance#dev-db", model.TypeRdsInstance)
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}
	f.notifier.publishErr = fmt.Errorf("sns down")

	summary := runEmpty(t, f)

	assert.Empty(t, summary.Errors, "notify failure must not surface as a reconcile error")
	require.Len(t, summary.Actions, 1, "action must still be recorded")
	status := f.db.Item("status#rds-instance#dev-db")
	require.NotNil(t, status)
	assert.Nil(t, status["last_error"], "notify failure must not be written as last_error")
}

// A-7: a PutStatus failure after a successful action is recorded as the cycle's error, and other resources are still reconciled (per-resource isolation holds even when persistence itself is what fails).
func TestPutStatusFailureAfterActionIsRecordedButIsolated(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-db", "rds-instance#dev-db", model.TypeRdsInstance)
	f.pinnedStoppedMember("b-fine", "rds-cluster#b-fine", model.TypeRdsCluster)
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}
	f.cluster.observations["b-fine"] = model.Observation{State: model.StateRunning}
	f.db.FailOn("update", "status#rds-instance#dev-db", fmt.Errorf("dynamodb unavailable"))

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "rds-instance#dev-db", summary.Errors[0].ResourceID)
	assert.Len(t, f.cluster.stopped, 1, "second resource must still be reconciled")
}

// A-7: when even the error-recording PutStatus fails, Run must not panic and must still process other resources; the failure is only logged.
func TestErrorRecordingFailureDoesNotPanic(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("a-broken", "rds-instance#a-broken", model.TypeRdsInstance)
	f.pinnedStoppedMember("b-fine", "rds-cluster#b-fine", model.TypeRdsCluster)
	f.rds.describeErr = fmt.Errorf("boom")
	f.cluster.observations["b-fine"] = model.Observation{State: model.StateRunning}
	f.db.FailOn("update", "status#rds-instance#a-broken", fmt.Errorf("dynamodb also down"))
	f.notifier.publishErr = fmt.Errorf("sns also down")

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "rds-instance#a-broken", summary.Errors[0].ResourceID)
	assert.Len(t, f.cluster.stopped, 1, "second resource must still be reconciled despite the first's reporting failing entirely")
}

func TestOneFailureDoesNotBreakOthers(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("a-broken", "rds-instance#a-broken", model.TypeRdsInstance)
	f.pinnedStoppedMember("b-fine", "rds-cluster#b-fine", model.TypeRdsCluster)
	f.rds.describeErr = fmt.Errorf("boom")
	f.cluster.observations["b-fine"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "rds-instance#a-broken", summary.Errors[0].ResourceID)
	assert.Len(t, f.cluster.stopped, 1, "second resource must still be reconciled")
}

func TestOverrideBeatsPinnedTag(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-db", "rds-instance#dev-db", model.TypeRdsInstance)
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("override#dev-db"), "desired": s(model.DesiredRunning),
		"expires_at": n(int(now.Add(time.Hour).Unix())),
	})
	f.rds.observations["dev-db"] = model.Observation{State: model.StateStopped}

	runEmpty(t, f)

	assert.Len(t, f.rds.started, 1, "override running must start the stopped instance")
}

func TestOverrideAppliesToEveryMember(t *testing.T) {
	f := newFixture(t)
	f.seedTag("dev", model.ModePinned, model.DesiredStopped)
	f.seedMember("dev", "rds-instance#dev-db", model.TypeRdsInstance)
	f.seedMember("dev", "ecs#dev-cluster/api", model.TypeEcsService)
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("override#dev"), "desired": s(model.DesiredRunning),
		"expires_at": n(int(now.Add(time.Hour).Unix())),
	})
	f.rds.observations["dev-db"] = model.Observation{State: model.StateStopped}
	f.ecs.observations["dev-cluster/api"] = model.Observation{State: model.StateStopped}

	runEmpty(t, f)

	assert.Equal(t, []string{"dev-db"}, f.rds.started)
	assert.Equal(t, []string{"dev-cluster/api"}, f.ecs.started)
}

func TestStatusAttrsFromTargetArePersisted(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-api", "ecs#dev/api", model.TypeEcsService)
	f.ecs.observations["dev/api"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)

	status := f.db.Item("status#ecs#dev/api")
	require.NotNil(t, status, "status item not written")
	assert.Equal(t, "3", status["saved_desired_count"].(*types.AttributeValueMemberN).Value)
}

// B-1: PrepareStop's saved state must be persisted before the mutating Stop call, so a crash (simulated here as Stop failing outright) still leaves a restorable saved_desired_count instead of none at all.
func TestSavedStateIsPersistedBeforeMutatingStopFails(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-api", "ecs#dev/api", model.TypeEcsService)
	f.ecs.observations["dev/api"] = model.Observation{State: model.StateRunning}
	f.ecs.stopErr = fmt.Errorf("boom: crashed mid-mutation")

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Empty(t, f.ecs.stopped, "Stop must have failed, not succeeded")
	status := f.db.Item("status#ecs#dev/api")
	require.NotNil(t, status, "saved state must be written even though Stop failed")
	assert.Equal(t, "3", status["saved_desired_count"].(*types.AttributeValueMemberN).Value)
}

// Proves member inheritance: a member added to an already-scheduled/pinned tag after the fact is
// acted on the very next reconcile, using the tag's existing config — no per-member setup needed.
func TestAddAfterScheduleAppliesOnNextReconcile(t *testing.T) {
	f := newFixture(t)
	f.seedTag("dev", model.ModePinned, model.DesiredStopped)
	f.seedMember("dev", "rds-instance#dev-db", model.TypeRdsInstance)
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)
	require.Len(t, f.rds.stopped, 1, "first member acted on")

	// A second member joins the same already-pinned tag, as if `cheapskate-cli add --tag dev ...`
	// had just run — no new tag or per-member config is written.
	f.seedMember("dev", "ecs#dev-cluster/api", model.TypeEcsService)
	f.ecs.observations["dev-cluster/api"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)

	assert.Equal(t, []string{"dev-cluster/api"}, f.ecs.stopped, "newly added member must inherit the tag's existing pin and be acted on immediately")
}

// A tag-level failure (an invalid cron here) is recorded and notified per member via the same
// path a per-member failure uses, so notify-once dedup (B-3) still holds per resource even though
// the underlying cause is shared.
func TestTagLevelErrorRecordedPerMemberWithNotifyOnceIntact(t *testing.T) {
	f := newFixture(t)
	f.seedTag("dev", model.ModeSchedule, "") // no start/stop cron: schedule.ResolveDesired errors
	f.seedMember("dev", "rds-instance#dev-db", model.TypeRdsInstance)
	f.seedMember("dev", "ecs#dev-cluster/api", model.TypeEcsService)

	summary := runEmpty(t, f)
	require.Len(t, summary.Errors, 2, "the tag-level error must be recorded once per member")
	assert.Len(t, f.notifier.published, 2, "and notified once per member")

	runEmpty(t, f)
	assert.Len(t, f.notifier.published, 2, "repeated identical tag-level error must not notify again")
}

// A member whose tag item is missing (data drift, or a mid-removal crash) must be skipped with a
// warning rather than crashing or silently mis-resolving to some default.
func TestOrphanedMemberIsSkippedWithWarning(t *testing.T) {
	f := newFixture(t)
	var logBuf bytes.Buffer
	f.deps.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	f.seedMember("ghost", "rds-instance#dev-db", model.TypeRdsInstance)
	f.rds.observations["dev-db"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	assert.Empty(t, f.rds.stopped, "orphaned member must not be acted on")
	assert.Empty(t, summary.Actions)
	assert.Empty(t, summary.Errors)
	assert.Contains(t, logBuf.String(), "orphaned-tag-data")
}

func loadFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return raw
}

func TestRdsEventReconcilesOnlyThatResource(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedMember("dev-aurora", "rds-cluster#dev-aurora", model.TypeRdsCluster)
	f.pinnedStoppedMember("other-db", "rds-instance#other-db", model.TypeRdsInstance)
	f.cluster.observations["dev-aurora"] = model.Observation{State: model.StateRunning}
	f.rds.observations["other-db"] = model.Observation{State: model.StateRunning}

	summary, err := Run(context.Background(), loadFixture(t, "rds-event-0153-cluster-autostart.json"), f.deps, now)
	require.NoError(t, err)

	assert.Equal(t, 1, summary.Reconciled)
	assert.Len(t, f.cluster.stopped, 1, "event resource must be stopped")
	assert.Empty(t, f.rds.stopped, "other resources must not be touched on an event")
}

// The RDS event fast path must isolate to the named member even when its tag has other members.
func TestRdsEventReconcilesOnlyThatMemberWithinSharedTag(t *testing.T) {
	f := newFixture(t)
	f.seedTag("dev", model.ModePinned, model.DesiredStopped)
	f.seedMember("dev", "rds-cluster#dev-aurora", model.TypeRdsCluster)
	f.seedMember("dev", "ecs#dev-cluster/api", model.TypeEcsService)
	f.cluster.observations["dev-aurora"] = model.Observation{State: model.StateRunning}
	f.ecs.observations["dev-cluster/api"] = model.Observation{State: model.StateRunning}

	summary, err := Run(context.Background(), loadFixture(t, "rds-event-0153-cluster-autostart.json"), f.deps, now)
	require.NoError(t, err)

	assert.Equal(t, 1, summary.Reconciled)
	assert.Len(t, f.cluster.stopped, 1)
	assert.Empty(t, f.ecs.stopped, "sibling member in the same tag must not be touched on an event")
}

func TestRdsEventForUnregisteredResourceIsIgnored(t *testing.T) {
	f := newFixture(t)
	summary, err := Run(context.Background(), loadFixture(t, "rds-event-0088-instance-started.json"), f.deps, now)
	require.NoError(t, err)
	assert.Equal(t, 0, summary.Reconciled)
	assert.Empty(t, summary.Errors)
}

// C-4: an event with a source we don't recognize (neither "" nor "aws.rds") must still fall
// back to a full reconcile, but log a warning so an operator notices an unexpected trigger.
func TestUnexpectedEventSourceWarnsAndFallsBackToFullReconcile(t *testing.T) {
	f := newFixture(t)
	var logBuf bytes.Buffer
	f.deps.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	f.pinnedStoppedMember("db1", "rds-instance#db1", model.TypeRdsInstance)
	f.rds.observations["db1"] = model.Observation{State: model.StateRunning}

	summary, err := Run(context.Background(), json.RawMessage(`{"source":"aws.partner/whatever"}`), f.deps, now)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Reconciled, "expected fallback to full reconcile")
	assert.Contains(t, logBuf.String(), "unexpected-event-source")
}

func TestRdsEventResourceID(t *testing.T) {
	var event Event
	require.NoError(t, json.Unmarshal(loadFixture(t, "rds-event-0088-instance-started.json"), &event))
	assert.Equal(t, "rds-instance#dev-db", RdsEventResourceID(event))
	assert.Empty(t, RdsEventResourceID(Event{Source: "aws.rds"}), "malformed event must map to empty")
}
