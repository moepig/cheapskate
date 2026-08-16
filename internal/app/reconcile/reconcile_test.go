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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/app/port"
	"cheapskate/internal/app/port/porttest"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
	mocks "cheapskate/internal/state/mocks"
)

func rdsInstance(ref string) model.Resource {
	return model.Resource{Type: model.TypeRdsInstance, Ref: ref}
}
func rdsCluster(ref string) model.Resource {
	return model.Resource{Type: model.TypeRdsCluster, Ref: ref}
}
func ecsService(ref string) model.Resource {
	return model.Resource{Type: model.TypeEcsService, Ref: ref}
}
func ec2Instance(ref string) model.Resource {
	return model.Resource{Type: model.TypeEc2Instance, Ref: ref}
}

type fixture struct {
	db         *mocks.DynaStore
	deps       *Deps
	rds        *porttest.Target
	cluster    *porttest.Target
	ecs        *porttest.Target
	ec2        *porttest.Target
	notifier   *porttest.Notifier
	discoverer *porttest.Discoverer
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	rds := porttest.NewTarget(model.TypeRdsInstance)
	cluster := porttest.NewTarget(model.TypeRdsCluster)
	ecs := porttest.NewTarget(model.TypeEcsService)
	ec2 := porttest.NewTarget(model.TypeEc2Instance)
	notifier := &porttest.Notifier{}
	discoverer := porttest.NewDiscoverer()
	return &fixture{
		db: db, rds: rds, cluster: cluster, ecs: ecs, ec2: ec2, notifier: notifier, discoverer: discoverer,
		deps: &Deps{
			Store:           state.New(api, "state"),
			Discoverer:      discoverer,
			Targets:         map[model.ResourceType]port.Target{rds.Typ: rds, cluster.Typ: cluster, ecs.Typ: ecs, ec2.Typ: ec2},
			Notifier:        notifier,
			DefaultTimezone: "UTC",
			Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		},
	}
}

func s[T ~string](v T) types.AttributeValue { return &types.AttributeValueMemberS{Value: string(v)} }
func n(v int) types.AttributeValue          { return &types.AttributeValueMemberN{Value: fmt.Sprint(v)} }

// セレクタのタグ値をグループ名と一致させた group# アイテムを用意する
// discoverer のテストダブルも同じキーを用いるため、f.discoverer.ByTagValue[name] がそのグループのメンバーとなる
// 別途のメンバー登録は不要である
func (f *fixture) seedGroup(name string, mode model.Mode, desired model.DesiredState) {
	f.db.Seed(map[string]types.AttributeValue{
		// pk の接頭辞は state 側のキー設計であり、status のリソース ID 名前空間である model.GroupNamespace ではない
		// 文字列は同一だが根拠が異なるため、共有しない (state/items.go を参照)
		"pk": s("group#" + name), "mode": s(mode), "desired": s(desired),
		"tag_key": s("env"), "tag_value": s(name),
		"types": &types.AttributeValueMemberSS{Value: model.TypeNames(model.KnownTypes)},
	})
}

// pinned かつ stopped のグループを用意し、その 1 リソースを discoverer へ結線する
// 本ファイルのテストは主に 1 リソースを扱うため、グループ名をそのリソースの識別にも用いる
func (f *fixture) pinnedStoppedGroup(name string, res model.Resource) {
	f.seedGroup(name, model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue[name] = []model.Resource{res}
}

var now = time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)

func runEmpty(t *testing.T, f *fixture) Summary {
	t.Helper()
	summary, err := Run(context.Background(), json.RawMessage(`{}`), f.deps, now)
	require.NoError(t, err)
	return summary
}

func TestStopsRunningPinnedResource(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	assert.Equal(t, []string{"dev-db"}, f.rds.Stopped)
	require.Len(t, summary.Actions, 1)
	assert.Equal(t, model.ActionStop, summary.Actions[0].Action)
	assert.Equal(t, "dev-db", summary.Actions[0].Group)
	status := f.db.Item("status#rds-instance#dev-db")
	require.NotNil(t, status, "status item not written")
	assert.Equal(t, "stop", status["last_action"].(*types.AttributeValueMemberS).Value)
	require.Len(t, f.notifier.Published, 1)
	assert.Equal(t, "[cheapskate] stop: dev-db/rds-instance#dev-db", f.notifier.Published[0].Subject)
}

// model.TypeEc2Instance を Deps.Targets 内の Target へ解決する経路を検証する
// 他の種別と同じ pin/stop のディスパッチを、ec2-instance についても通す
func TestStopsRunningPinnedEc2Instance(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-vm", ec2Instance("i-0abc123"))
	f.ec2.Observations["i-0abc123"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	assert.Equal(t, []string{"i-0abc123"}, f.ec2.Stopped)
	require.Len(t, summary.Actions, 1)
	assert.Equal(t, model.ActionStop, summary.Actions[0].Action)
	status := f.db.Item("status#ec2-instance#i-0abc123")
	require.NotNil(t, status, "status item not written")
	assert.Equal(t, "stop", status["last_action"].(*types.AttributeValueMemberS).Value)
}

// terminated 状態の EC2 インスタンスは、Tagging API から 1 時間程度は返り続ける
// Ec2InstanceTarget.Describe は "terminated" を StateNotFound へ写像し (ec2.go を参照)、reconcile はこれをスキップとして扱う
// TestNotFoundAfterDiscoverySkipsWithoutError と同じ経路を、実際の EC2 ターゲットで検証する
func TestTerminatedEc2InstanceSkippedWithoutError(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-vm", ec2Instance("i-0abc123"))
	f.ec2.Observations["i-0abc123"] = model.Observation{State: model.StateNotFound}

	summary := runEmpty(t, f)

	assert.Empty(t, summary.Errors)
	assert.Empty(t, summary.Actions)
	assert.Empty(t, f.ec2.Stopped)
	assert.Nil(t, f.db.Item("status#ec2-instance#i-0abc123"), "a skip must not write status")
	assert.Empty(t, f.notifier.Published, "a skip must never notify")
}

func TestStopsAllResourcesOfPinnedGroup(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue["dev"] = []model.Resource{rdsInstance("dev-db"), ecsService("dev-cluster/api")}
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateRunning}
	f.ecs.Observations["dev-cluster/api"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	assert.Equal(t, []string{"dev-db"}, f.rds.Stopped)
	assert.Equal(t, []string{"dev-cluster/api"}, f.ecs.Stopped)
	require.Len(t, summary.Actions, 2)
	for _, a := range summary.Actions {
		assert.Equal(t, "dev", a.Group)
	}
	require.NotNil(t, f.db.Item("status#rds-instance#dev-db"), "status stays per-resource")
	require.NotNil(t, f.db.Item("status#ecs-service#dev-cluster/api"), "status stays per-resource")
	assert.Len(t, f.notifier.Published, 2)
}

func TestConvergedWritesAndNotifiesNothing(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateStopped}

	summary := runEmpty(t, f)

	assert.Empty(t, summary.Actions)
	assert.Empty(t, summary.Errors)
	assert.Nil(t, f.db.Item("status#rds-instance#dev-db"), "converged cycle must not write status")
	assert.Empty(t, f.notifier.Published, "converged cycle must not notify")
}

func TestTransitioningIsSkipped(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateTransitioning, Detail: "stopping"}

	summary := runEmpty(t, f)

	assert.Empty(t, f.rds.Stopped)
	assert.Empty(t, f.rds.Started)
	assert.Empty(t, summary.Actions)
	assert.Empty(t, summary.Errors)
}

// disabled のグループでは Discover を呼んではならない
// 収束の対象が存在しないため、セレクタの解決と AWS Tagging API への呼び出しはいずれも不要である
// 加えて、そのグループが持たない権限を要求する場合がある
func TestDisabledGroupSkipsDiscoveryAndAllResources(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModeDisabled, "")

	runEmpty(t, f)

	assert.Zero(t, f.discoverer.Calls(), "disabled group must never call Discover")
	assert.Empty(t, f.rds.Stopped)
	assert.Empty(t, f.ecs.Stopped)
}

// 探索の直後に消えるリソースは、エラーではなくスキップとして扱う
// 削除との競合、または Tagging API の反映遅延によるものである
// 廃止したメンバー登録モデルでは、登録済みのリソースの消失は設定の不整合を示すエラーであった
func TestNotFoundAfterDiscoverySkipsWithoutError(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("gone", rdsInstance("gone"))
	// 観測値が未設定であるため、porttest.Target.Describe は StateNotFound を返す

	summary := runEmpty(t, f)

	assert.Empty(t, summary.Errors)
	require.Len(t, summary.Actions, 0)
	assert.Nil(t, f.db.Item("status#rds-instance#gone"), "a skip must not write status")
	assert.Empty(t, f.notifier.Published, "a skip must never notify")
}

// 内容が変わらないエラーは、毎サイクルではなく 1 度だけ通知しなければならない
func TestRepeatedSameErrorNotifiesOnce(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("broken", rdsInstance("broken"))
	f.rds.DescribeErr = fmt.Errorf("access denied")

	runEmpty(t, f)
	runEmpty(t, f)

	assert.Len(t, f.notifier.Published, 1, "repeated identical error must notify once")
}

// エラーメッセージが変化した場合は、改めて通知しなければならない
func TestChangedErrorNotifiesAgain(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.DescribeErr = fmt.Errorf("first failure")

	runEmpty(t, f)

	f.rds.DescribeErr = fmt.Errorf("second, different failure")
	runEmpty(t, f)

	assert.Len(t, f.notifier.Published, 2, "changed error must notify again")
}

// エラー状態のリソースが収束した場合、復旧通知を 1 度だけ送り、last_error を削除する
func TestRecoveryNotifiesOnceAndClearsError(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.DescribeErr = fmt.Errorf("transient failure")

	runEmpty(t, f)
	require.Len(t, f.notifier.Published, 1, "initial error must notify")

	f.rds.DescribeErr = nil
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateStopped} // desired=stopped に対してすでに収束済み
	runEmpty(t, f)

	require.Len(t, f.notifier.Published, 2, "recovery must notify once")
	assert.Equal(t, "[cheapskate] recovered: dev-db/rds-instance#dev-db", f.notifier.Published[1].Subject)
	status := f.db.Item("status#rds-instance#dev-db")
	assert.Equal(t, "", status["last_error"].(*types.AttributeValueMemberS).Value, "last_error must be cleared")

	runEmpty(t, f)
	assert.Len(t, f.notifier.Published, 2, "already-recovered convergence must not notify again")
}

// performAction の default 節は Run 経由では到達しない
// model.DecideAction が返すのは "stop"、"start"、"" に限るためである
// この節は、model.Action に値が追加されたとき、未知のアクションを Target へ送らないために存在する
func TestPerformActionRejectsUnknownAction(t *testing.T) {
	tgt := porttest.NewTarget(model.TypeRdsInstance)
	err := performAction(context.Background(), model.Resource{}, "pause", tgt)
	assert.ErrorContains(t, err, `unknown action "pause"`)
	assert.Empty(t, tgt.Stopped, "the unknown-action branch must not call Stop")
	assert.Empty(t, tgt.Started, "the unknown-action branch must not call Start")
}

// start/stop のアクションの成功が直前のエラーを解消した場合、復旧通知は送らない
// アクション自身の通知が復旧を伝えるためである
// 収束済みかつアクションなしの経路 (TestRecoveryNotifiesOnceAndClearsError) では、復旧通知を送る
func TestActionSuccessClearsPriorErrorWithoutSeparateRecoveredNotification(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.DescribeErr = fmt.Errorf("transient failure")

	runEmpty(t, f)
	require.Len(t, f.notifier.Published, 1, "initial error must notify")

	f.rds.DescribeErr = nil
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateRunning} // まだ stop のアクションが必要
	runEmpty(t, f)

	require.Len(t, f.notifier.Published, 2, "the stop action's own notification must cover recovery")
	assert.Equal(t, "[cheapskate] stop: dev-db/rds-instance#dev-db", f.notifier.Published[1].Subject)
	status := f.db.Item("status#rds-instance#dev-db")
	require.NotNil(t, status)
	assert.Equal(t, "", status["last_error"].(*types.AttributeValueMemberS).Value, "last_error must be cleared even without a distinct notification")
}

// 復旧したエラーの削除における PutStatus の失敗は、ログへの記録のみとし、reconcile のエラーとしても panic としてもならない
// 過去のエラーの記録の更新が失敗しただけであり、リソース自体は収束しているためである
func TestClearRecoveredErrorPutStatusFailureIsLoggedNotSurfaced(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.DescribeErr = fmt.Errorf("transient failure")
	runEmpty(t, f)
	require.Len(t, f.notifier.Published, 1)

	f.rds.DescribeErr = nil
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateStopped} // desired=stopped に対してすでに収束済み
	f.db.FailOn("update", "status#rds-instance#dev-db", fmt.Errorf("dynamodb unavailable"))

	summary := runEmpty(t, f)

	assert.Empty(t, summary.Errors, "a failure clearing the recovered error must not surface as a reconcile error")
	assert.Len(t, f.notifier.Published, 1, "no recovered notification when the clearing PutStatus itself failed")
	status := f.db.Item("status#rds-instance#dev-db")
	require.NotNil(t, status)
	assert.NotEqual(t, "", status["last_error"].(*types.AttributeValueMemberS).Value, "last_error must remain since the clear failed")
}

// アクションの成功後における Publish の失敗を、reconcile のエラーとして記録してはならない
// アクションは成功し、永続化も完了しているためである
func TestNotifyFailureAfterSuccessfulActionIsNotAnError(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateRunning}
	f.notifier.Err = fmt.Errorf("sns down")

	summary := runEmpty(t, f)

	assert.Empty(t, summary.Errors, "notify failure must not surface as a reconcile error")
	require.Len(t, summary.Actions, 1, "action must still be recorded")
	status := f.db.Item("status#rds-instance#dev-db")
	require.NotNil(t, status)
	assert.Nil(t, status["last_error"], "notify failure must not be written as last_error")
}

// アクションの成功後における PutStatus の失敗は、そのサイクルのエラーとして記録し、他のリソースの reconcile を継続する
// 永続化が失敗した場合も、リソース単位の隔離を保つ
func TestPutStatusFailureAfterActionIsRecordedButIsolated(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.pinnedStoppedGroup("b-fine", rdsCluster("b-fine"))
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateRunning}
	f.cluster.Observations["b-fine"] = model.Observation{State: model.StateRunning}
	f.db.FailOn("update", "status#rds-instance#dev-db", fmt.Errorf("dynamodb unavailable"))

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "rds-instance#dev-db", summary.Errors[0].ResourceID)
	assert.Len(t, f.cluster.Stopped, 1, "second resource must still be reconciled")
}

// エラー記録用の PutStatus が失敗した場合も、Run は panic せず他のリソースの処理を継続しなければならない
// その失敗はログへ記録するのみとする
func TestErrorRecordingFailureDoesNotPanic(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("a-broken", rdsInstance("a-broken"))
	f.pinnedStoppedGroup("b-fine", rdsCluster("b-fine"))
	f.rds.DescribeErr = fmt.Errorf("boom")
	f.cluster.Observations["b-fine"] = model.Observation{State: model.StateRunning}
	f.db.FailOn("update", "status#rds-instance#a-broken", fmt.Errorf("dynamodb also down"))
	f.notifier.Err = fmt.Errorf("sns also down")

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "rds-instance#a-broken", summary.Errors[0].ResourceID)
	assert.Len(t, f.cluster.Stopped, 1, "second resource must still be reconciled despite the first's reporting failing entirely")
}

func TestOneFailureDoesNotBreakOthers(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("a-broken", rdsInstance("a-broken"))
	f.pinnedStoppedGroup("b-fine", rdsCluster("b-fine"))
	f.rds.DescribeErr = fmt.Errorf("boom")
	f.cluster.Observations["b-fine"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "rds-instance#a-broken", summary.Errors[0].ResourceID)
	assert.Len(t, f.cluster.Stopped, 1, "second resource must still be reconciled")
}

func TestOverrideBeatsPinnedGroup(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("override#dev-db"), "desired": s(model.DesiredRunning),
		"expires_at": n(int(now.Add(time.Hour).Unix())),
	})
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateStopped}

	runEmpty(t, f)

	assert.Len(t, f.rds.Started, 1, "override running must start the stopped instance")
}

func TestOverrideAppliesToEveryResourceInGroup(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue["dev"] = []model.Resource{rdsInstance("dev-db"), ecsService("dev-cluster/api")}
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("override#dev"), "desired": s(model.DesiredRunning),
		"expires_at": n(int(now.Add(time.Hour).Unix())),
	})
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateStopped}
	f.ecs.Observations["dev-cluster/api"] = model.Observation{State: model.StateStopped}

	runEmpty(t, f)

	assert.Equal(t, []string{"dev-db"}, f.rds.Started)
	assert.Equal(t, []string{"dev-cluster/api"}, f.ecs.Started)
}

// disabled は override より優先度の高い停止である
// disable は override# アイテムを削除しないため、pin → override → disable の順の操作により、disabled のグループに未失効の override が残る状態となる
// この経路で reconciler が override を適用した場合、停止したグループが override の失効まで起動する
func TestDisabledGroupIgnoresLiveOverride(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModeDisabled, "")
	f.discoverer.ByTagValue["dev"] = []model.Resource{rdsInstance("dev-db")}
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("override#dev"), "desired": s(model.DesiredRunning),
		"expires_at": n(int(now.Add(time.Hour).Unix())),
	})
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateStopped}

	summary := runEmpty(t, f)

	assert.Empty(t, f.rds.Started, "disabled group must not start anything, even with a live override")
	assert.Zero(t, f.discoverer.Calls(), "disabled group must never call Discover")
	assert.Empty(t, summary.Errors)
}

func TestStatusAttrsFromTargetArePersisted(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-api", ecsService("dev/api"))
	f.ecs.Observations["dev/api"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)

	status := f.db.Item("status#ecs-service#dev/api")
	require.NotNil(t, status, "status item not written")
	assert.Equal(t, "running", status["observed_state"].(*types.AttributeValueMemberS).Value)
	assert.Equal(t, "stop", status["last_action"].(*types.AttributeValueMemberS).Value)
}

// Stop の前に復元用の状態を先行して書き込むことはしない
// ECS の起動時 desired count とスケーリングの上下限は、保存したステータスではなくリソース自身のタグから取得するためである
// したがって Stop の失敗が残すのは、記録されたエラーのみでなければならない
// last_action と observed_state はアクションの成功後にのみ書き込むため、残ってはならない
func TestFailedStopRecordsOnlyTheError(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-api", ecsService("dev/api"))
	f.ecs.Observations["dev/api"] = model.Observation{State: model.StateRunning}
	f.ecs.StopErr = fmt.Errorf("boom: crashed mid-mutation")

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Empty(t, f.ecs.Stopped, "Stop must have failed, not succeeded")
	status := f.db.Item("status#ecs-service#dev/api")
	require.NotNil(t, status, "error must be recorded")
	_, hasLastAction := status["last_action"]
	assert.False(t, hasLastAction, "a failed stop must not record last_action")
	_, hasObservedState := status["observed_state"]
	assert.False(t, hasObservedState, "a failed stop must not record observed_state")
}

// 動的探索による設定の継承を検証する
// pinned または schedule のグループのセレクタに新たに一致したリソースは、次の reconcile で操作の対象となる
// グループとスケジュールの設定を変更せず、リソースへのタグ付与のみを行った場合が該当する
func TestNewlyDiscoveredResourceInheritsGroupsExistingPinOnNextReconcile(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue["dev"] = []model.Resource{rdsInstance("dev-db")}
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)
	require.Len(t, f.rds.Stopped, 1, "first resource acted on")

	// 2 つめのリソースが、pinned である同じグループのセレクタに一致する
	// リソースへタグを付与した状態であり、グループ設定は変更していない
	f.discoverer.ByTagValue["dev"] = []model.Resource{rdsInstance("dev-db"), ecsService("dev-cluster/api")}
	f.ecs.Observations["dev-cluster/api"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)

	assert.Equal(t, []string{"dev-cluster/api"}, f.ecs.Stopped, "newly discovered resource must inherit the group's existing pin and be acted on immediately")
}

// グループ単位の失敗は、リソースごとではなく "group#<name>" のステータスへ 1 度だけ記録する
// resolveGroup が失敗した時点で Discover は呼ばれもしないためである
// 通知の重複排除はグループ単位でも同じように効く
func TestGroupLevelErrorRecordedOnceNotPerResource(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModeSchedule, "") // start/stop の cron がないため schedule.ResolveDesired が失敗する

	summary := runEmpty(t, f)
	require.Len(t, summary.Errors, 1, "a group-level error must be recorded once, not per resource")
	assert.Equal(t, "dev", summary.Errors[0].Group)
	assert.Empty(t, summary.Errors[0].ResourceID, "a group-level error has no single resource_id")
	assert.Zero(t, f.discoverer.Calls(), "Discover must never be called once group resolution fails")
	require.Len(t, f.notifier.Published, 1)

	status := f.db.Item("status#group#dev")
	require.NotNil(t, status, "group-level failure recorded under status#group#<name>")

	runEmpty(t, f)
	assert.Len(t, f.notifier.Published, 1, "repeated identical group-level error must not notify again")
}

// あるグループ配下のアイテム(ここでは override)が壊れていても、他のグループの reconcile を止めてはならない
// 壊れたグループについては desired が確定できないので、推測して操作するのではなく必ず何もしない
// 誤った向きへ倒すと、止めるべきでないものを止める・起こすべきでないものを起こすことになる
// 失敗はグループ単位のエラーと同じ経路(status#group#<name>)へ記録し、オペレータに届ける
func TestCorruptGroupRecordIsRecordedAndDoesNotTouchItsResources(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("broken", rdsInstance("broken-db"))
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("override#broken"), "desired": s(model.DesiredRunning),
		"expires_at": s("not-a-number"), // 数値でなければならず、UnmarshalMap が失敗する
	})
	f.rds.Observations["broken-db"] = model.Observation{State: model.StateRunning}
	f.pinnedStoppedGroup("fine", rdsInstance("fine-db"))
	f.rds.Observations["fine-db"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "broken", summary.Errors[0].Group)
	assert.Empty(t, summary.Errors[0].ResourceID, "破損はグループ単位の失敗であってリソースの問題ではない")
	assert.Equal(t, []string{"fine-db"}, f.rds.Stopped, "壊れたグループのリソースには触れず、他のグループは通常どおり収束する")
	assert.NotNil(t, f.db.Item("status#group#broken"), "破損は status#group#<name> へ記録される")
	assert.Nil(t, f.db.Item("status#rds-instance#broken-db"), "リソース側のステータスは書かれない")

	// 破損が直るまで毎サイクル同じエラーが出続けるので、通知は 1 度きりでなければならない
	runEmpty(t, f)
	var notified int
	for _, p := range f.notifier.Published {
		if strings.Contains(p.Subject, "broken") {
			notified++
		}
	}
	assert.Equal(t, 1, notified, "repeated identical corruption must not notify again")
}

// 孤立データは警告を伴ってスキップしなければならない
// 対応する group# アイテムを持たない override などであり、削除の中断により生じる
// 処理の中断も、既定値による解決も行ってはならない
func TestOrphanedGroupDataIsSkippedWithWarning(t *testing.T) {
	f := newFixture(t)
	var logBuf bytes.Buffer
	f.deps.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	f.db.Seed(map[string]types.AttributeValue{
		"pk": s("override#ghost"), "desired": s(model.DesiredRunning),
		"expires_at": n(int(now.Add(time.Hour).Unix())),
	})

	summary := runEmpty(t, f)

	assert.Empty(t, summary.Actions)
	assert.Empty(t, summary.Errors)
	assert.Contains(t, logBuf.String(), "orphaned-group-data")
}

func loadFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return raw
}

// RDS イベント時に単一リソースへ絞る reconcile は、メンバー登録とともに廃止した
// 絞り込みに使える O(1) の リソース -> グループ 逆引きがもう存在しないためである
// 現在は RDS かどうかを問わず、すべてのイベントが全グループの完全な reconcile を起動する
func TestRdsEventTriggersFullReconcile(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-aurora", rdsCluster("dev-aurora"))
	f.pinnedStoppedGroup("other-db", rdsInstance("other-db"))
	f.cluster.Observations["dev-aurora"] = model.Observation{State: model.StateRunning}
	f.rds.Observations["other-db"] = model.Observation{State: model.StateRunning}

	summary, err := Run(context.Background(), loadFixture(t, "rds-event-0151-cluster-started.json"), f.deps, now)
	require.NoError(t, err)

	assert.Equal(t, 2, summary.Reconciled)
	assert.Len(t, f.cluster.Stopped, 1)
	assert.Len(t, f.rds.Stopped, 1, "an RDS event must reconcile every group, not just the resource it names")
}

// 既知かどうかを問わず、すべてのイベントソースで全体 reconcile を行う
// 単一リソースへ絞り込む経路は存在しない
func TestEventSourcePresentStillFullReconciles(t *testing.T) {
	f := newFixture(t)
	var logBuf bytes.Buffer
	f.deps.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	f.pinnedStoppedGroup("db1", rdsInstance("db1"))
	f.rds.Observations["db1"] = model.Observation{State: model.StateRunning}

	summary, err := Run(context.Background(), json.RawMessage(`{"source":"aws.partner/whatever"}`), f.deps, now)
	require.NoError(t, err)
	assert.Equal(t, 1, summary.Reconciled)
	assert.Contains(t, logBuf.String(), "event-received")
}

// セレクタ重複の防護策として、複数グループのセレクタにマッチしたリソースは名前順で最初のグループが取得する
// 取得できなかったグループは、二重の管理を行わず、リソース単位のエラーを受け取る
func TestSelectorOverlapFirstGroupWinsBySortedName(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("a-first", model.ModePinned, model.DesiredStopped)
	f.seedGroup("z-second", model.ModePinned, model.DesiredRunning)
	shared := rdsInstance("shared-db")
	f.discoverer.ByTagValue["a-first"] = []model.Resource{shared}
	f.discoverer.ByTagValue["z-second"] = []model.Resource{shared}
	f.rds.Observations["shared-db"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	assert.Equal(t, []string{"shared-db"}, f.rds.Stopped, "the first group (a-first, pinned stopped) must act on the shared resource")
	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "z-second", summary.Errors[0].Group, "the losing group must get the per-resource error")
	assert.Equal(t, "rds-instance#shared-db", summary.Errors[0].ResourceID)
}

// セレクタ重複のエラーは、リソースが共有する status# ではなく、報告する側のグループの status#group# に記録しなければならない
// 共有アイテムへ書いた場合、そのリソースを所有するグループの clearRecoveredError と同じアイテムを毎サイクル更新することになる
func TestSelectorOverlapRecordsOnGroupStatusNotOnSharedResource(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("a-first", model.ModePinned, model.DesiredStopped)
	f.seedGroup("z-second", model.ModePinned, model.DesiredRunning)
	shared := rdsInstance("shared-db")
	f.discoverer.ByTagValue["a-first"] = []model.Resource{shared}
	f.discoverer.ByTagValue["z-second"] = []model.Resource{shared}
	f.rds.Observations["shared-db"] = model.Observation{State: model.StateStopped} // a-first から見て収束済み

	runEmpty(t, f)

	assert.Nil(t, f.db.Item("status#rds-instance#shared-db"),
		"the shared resource's status must stay owned by the group that claimed it")
	groupStatus := f.db.Item("status#group#z-second")
	require.NotNil(t, groupStatus, "the losing group must record the overlap on its own status")
	assert.Contains(t, groupStatus["last_error"].(*types.AttributeValueMemberS).Value, "shared-db")
}

// セレクタ重複が続いている間、通知は最初の 1 回だけでなければならない
// 記録先が共有の status# だった頃は、所有グループのクリアと報告グループの記録が毎サイクル交互に走り、「recovered」と「error」を 5 分おきに永久に鳴らし続けていた
func TestSelectorOverlapDoesNotFlapNotifications(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("a-first", model.ModePinned, model.DesiredStopped)
	f.seedGroup("z-second", model.ModePinned, model.DesiredRunning)
	shared := rdsInstance("shared-db")
	f.discoverer.ByTagValue["a-first"] = []model.Resource{shared}
	f.discoverer.ByTagValue["z-second"] = []model.Resource{shared}
	f.rds.Observations["shared-db"] = model.Observation{State: model.StateStopped}

	runEmpty(t, f)
	require.Len(t, f.notifier.Published, 1, "the overlap must be reported once")
	assert.Contains(t, f.notifier.Published[0].Subject, "error: z-second")

	runEmpty(t, f) // 何も変わっていない 2 サイクル目
	runEmpty(t, f)

	assert.Len(t, f.notifier.Published, 1, "an unchanged overlap must never notify again")
}

// 遷移中を最初に観測したサイクルで transitioning_since を 1 回だけ書く
// 遷移中のリソースは毎サイクル skip されるので、無条件に書くと定常状態の書き込みを避けた設計が崩れる
func TestTransitioningRecordsSinceOnceThenClearsOnConvergence(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateTransitioning, Detail: "stopping"}

	require.NoError(t, runAt(t, f, now))
	since := statusAttr(t, f, "status#rds-instance#dev-db", "transitioning_since")
	assert.Equal(t, now.Format(time.RFC3339), since)

	require.NoError(t, runAt(t, f, now.Add(10*time.Minute))) // まだ遷移中
	assert.Equal(t, since, statusAttr(t, f, "status#rds-instance#dev-db", "transitioning_since"),
		"an already-recorded transition must not be re-stamped every cycle")

	f.rds.Observations["dev-db"] = model.Observation{State: model.StateStopped}
	require.NoError(t, runAt(t, f, now.Add(20*time.Minute)))
	assert.Empty(t, statusAttr(t, f, "status#rds-instance#dev-db", "transitioning_since"),
		"reaching a settled state must clear the transition marker")
	assert.Empty(t, f.notifier.Published, "a stuck transition is surfaced by doctor, never by notification")
}

// transitioning_since の記録と消去は、いずれもベストエフォートである
// これは監査のための情報であり収束の判断には用いないため、書き込みの失敗を理由にそのサイクルをエラーとしてはならず、他のリソースの処理も止めてはならない
// 失敗の理由は必ずログへ記録する
func TestTransitioningMarkerFailuresAreLoggedNotSurfaced(t *testing.T) {
	t.Run("mark", func(t *testing.T) {
		f := newFixture(t)
		var logBuf bytes.Buffer
		f.deps.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
		f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
		f.pinnedStoppedGroup("b-fine", rdsCluster("b-fine"))
		f.rds.Observations["dev-db"] = model.Observation{State: model.StateTransitioning, Detail: "stopping"}
		f.cluster.Observations["b-fine"] = model.Observation{State: model.StateRunning}
		f.db.FailOn("update", "status#rds-instance#dev-db", fmt.Errorf("dynamodb unavailable"))

		summary := runEmpty(t, f)

		assert.Empty(t, summary.Errors, "監査情報の書き込み失敗を収束の失敗にしてはならない")
		assert.Contains(t, logBuf.String(), "transitioning-mark-failed")
		assert.Len(t, f.cluster.Stopped, 1, "他のリソースの reconcile は続く")
	})

	t.Run("clear", func(t *testing.T) {
		f := newFixture(t)
		var logBuf bytes.Buffer
		f.deps.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
		f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
		f.rds.Observations["dev-db"] = model.Observation{State: model.StateTransitioning}
		require.NoError(t, runAt(t, f, now)) // transitioning_since を残す

		f.rds.Observations["dev-db"] = model.Observation{State: model.StateStopped} // desired=stopped に収束
		f.db.FailOn("update", "status#rds-instance#dev-db", fmt.Errorf("dynamodb unavailable"))

		summary := runEmpty(t, f)

		assert.Empty(t, summary.Errors)
		assert.Contains(t, logBuf.String(), "transitioning-clear-failed")
	})
}

// 探索できたリソースの種別に対応する Target が結線されていない状態は、結線側の不備である
// エラーを記録せずにスキップした場合、そのリソースは収束せず、検知の経路も存在しない
// リソース単位のエラーとして記録し、他のリソースは通常どおり処理する
func TestUnknownResourceTypeIsRecordedPerResource(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue["dev"] = []model.Resource{
		{Type: "sqs-queue", Ref: "unwired"}, // Deps.Targets にない種別
		rdsInstance("dev-db"),
	}
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "sqs-queue#unwired", summary.Errors[0].ResourceID)
	assert.Contains(t, summary.Errors[0].Error, `no target for type "sqs-queue"`)
	assert.Equal(t, []string{"dev-db"}, f.rds.Stopped, "結線漏れが同じグループの他のリソースを巻き込んではならない")
}

// 探索の失敗はグループ全体の失敗である
// どのリソースが属するか分からない以上、一部だけ操作するのではなく何もしない
// 記録先はリソースではなくグループ単位のステータスになる
func TestDiscoverFailureIsRecordedOnTheGroup(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ErrByTagValue["dev"] = fmt.Errorf("AccessDenied")
	f.pinnedStoppedGroup("fine", rdsInstance("fine-db"))
	f.rds.Observations["fine-db"] = model.Observation{State: model.StateRunning}

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Equal(t, "dev", summary.Errors[0].Group)
	assert.Empty(t, summary.Errors[0].ResourceID)
	assert.Contains(t, summary.Errors[0].Error, "AccessDenied")
	assert.NotNil(t, f.db.Item("status#group#dev"))
	assert.Equal(t, []string{"fine-db"}, f.rds.Stopped, "探索できたグループは通常どおり収束する")
}

// 復旧通知の Publish が失敗しても、last_error はすでに消えている
// この通知は「直りました」と伝えるだけのものなので、送れなかったことを理由に直っていない状態へ巻き戻したり、そのサイクルをエラーにしたりしてはならない
// 次のサイクルは last_error が空なので復旧通知を再送しない(重複排除の代償として受け入れる)
func TestRecoveredNotifyFailureLeavesErrorCleared(t *testing.T) {
	f := newFixture(t)
	var logBuf bytes.Buffer
	f.deps.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.DescribeErr = fmt.Errorf("transient failure")
	runEmpty(t, f) // last_error を残す

	f.rds.DescribeErr = nil
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateStopped} // desired=stopped に収束
	f.notifier.Err = fmt.Errorf("sns down")

	summary := runEmpty(t, f)

	assert.Empty(t, summary.Errors, "通知が送れないことは収束の失敗ではない")
	assert.Contains(t, logBuf.String(), "recovery-notify-failed")
	assert.Equal(t, "", statusAttr(t, f, "status#rds-instance#dev-db", "last_error"),
		"PutStatus は成功しているので last_error は消えたままでなければならない")
}

// 通知の重複排除は前回の last_error との一致で決まるため、前回を読めない場合は判定できない
// 読めない場合を一致として扱うと、初回の通知も抑止される
// 判定できない場合は通知を行い、読めなかった事実をログへ記録する
func TestErrorReportingNotifiesWhenPreviousStatusCannotBeRead(t *testing.T) {
	f := newFixture(t)
	var logBuf bytes.Buffer
	f.deps.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	f.seedGroup("dev", model.ModeSchedule, "") // start/stop の cron がないため resolveGroup が失敗する
	f.db.FailOn("get", "status#group#dev", fmt.Errorf("dynamodb unavailable"))

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Contains(t, logBuf.String(), "status-read-failed")
	assert.Len(t, f.notifier.Published, 1, "重複排除を判定できない場合は通知する")
}

// 起動が成立しない失敗は、空の Summary による成功ではなく Run のエラーとする
// イベントの読み取り失敗とテーブルの読み取り失敗が該当する
// Lambda の呼び出しを失敗させ、再試行とアラームの対象とするためである
func TestRunAbortsOnUnusableInput(t *testing.T) {
	t.Run("malformed event", func(t *testing.T) {
		f := newFixture(t)

		_, err := Run(context.Background(), json.RawMessage(`{not json`), f.deps, now)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshal event")
		assert.Zero(t, f.discoverer.Calls(), "イベントが読めない時点で何も収束させてはならない")
	})

	t.Run("scan failure", func(t *testing.T) {
		f := newFixture(t)
		f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
		f.db.FailOn("scan", "", fmt.Errorf("dynamodb unavailable"))

		_, err := Run(context.Background(), json.RawMessage(`{}`), f.deps, now)

		require.Error(t, err)
		assert.Empty(t, f.rds.Stopped, "設定が読めない状態で何かを操作してはならない")
	})
}

func runAt(t *testing.T, f *fixture, at time.Time) error {
	t.Helper()
	_, err := Run(context.Background(), json.RawMessage(`{}`), f.deps, at)
	return err
}

func statusAttr(t *testing.T, f *fixture, pk, attr string) string {
	t.Helper()
	item := f.db.Item(pk)
	require.NotNil(t, item, "status item %s not written", pk)
	v, ok := item[attr]
	if !ok {
		return ""
	}
	return v.(*types.AttributeValueMemberS).Value
}
