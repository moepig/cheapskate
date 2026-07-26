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

// セレクタのタグ値をグループ名そのものにした group# アイテムを用意する
// discoverer のテストダブルも同じキーで引くので、f.discoverer.ByTagValue[name] = ... がそのままこのグループの「中身」になる
// 別途のメンバー登録は必要ない
func (f *fixture) seedGroup(name string, mode model.Mode, desired model.DesiredState) {
	f.db.Seed(map[string]types.AttributeValue{
		// pk の接頭辞は state 側のキー設計であって、model.GroupNamespace（status のリソース ID 名前空間）ではない
		// 同じ文字列だが理由が異なるので、あえて結合しない（state/items.go を参照）
		"pk": s("group#" + name), "mode": s(mode), "desired": s(desired),
		"tag_key": s("env"), "tag_value": s(name),
		"types": &types.AttributeValueMemberSS{Value: model.TypeNames(model.KnownTypes)},
	})
}

// pinned かつ stopped のグループを用意し、その 1 リソースを discoverer に結線する
// ここのテストはほぼ 1 リソースだけを扱うので、グループ名がそのリソースの読みやすい呼び名も兼ねる
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

// EC2 はこれまで reconcile 層のテストに結線されていなかった
// 汎用の pin/stop ディスパッチ、つまり model.TypeEc2Instance を Deps.Targets 内の Target へ解決する処理のことである
// これまでは rds-instance・rds-cluster・ecs-service でしか通されていなかった
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

// terminated 状態の EC2 インスタンスは、Tagging API 経由では 1 時間ほど見え続ける
// しかし Ec2InstanceTarget.Describe は "terminated" を StateNotFound へ写像し（ec2.go を参照）、reconcile はこれを穏当なスキップとして扱う
// TestNotFoundAfterDiscoverySkipsWithoutError と同じ筋書きを、汎用の代用品ではなく実際の EC2 ターゲットで通している
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

// disabled のグループでは Discover を一切呼んではならない
// 収束させる対象がないので、セレクタの解決も AWS Tagging API への往復も純粋な無駄になる
// しかもそのグループがまだ持っていないかもしれない権限を要求してしまう
func TestDisabledGroupSkipsDiscoveryAndAllResources(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModeDisabled, "")

	runEmpty(t, f)

	assert.Zero(t, f.discoverer.Calls(), "disabled group must never call Discover")
	assert.Empty(t, f.rds.Stopped)
	assert.Empty(t, f.ecs.Stopped)
}

// 探索直後に消えるリソース（削除との競合や Tagging API の遅れ）はエラーではなく穏当なスキップとして扱う
// 廃止したメンバー登録モデルでは、明示的に管理を指示されたリソースが消えることは本物の設定ずれのエラーだった
func TestNotFoundAfterDiscoverySkipsWithoutError(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("gone", rdsInstance("gone"))
	// 観測値を用意していないので、porttest.Target.Describe は既定の StateNotFound を返す

	summary := runEmpty(t, f)

	assert.Empty(t, summary.Errors)
	require.Len(t, summary.Actions, 0)
	assert.Nil(t, f.db.Item("status#rds-instance#gone"), "a skip must not write status")
	assert.Empty(t, f.notifier.Published, "a skip must never notify")
}

// 内容が変わらないエラー（Describe が常に失敗するなど）は、毎サイクルではなく 1 度だけ通知しなければならない
func TestRepeatedSameErrorNotifiesOnce(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("broken", rdsInstance("broken"))
	f.rds.DescribeErr = fmt.Errorf("access denied")

	runEmpty(t, f)
	runEmpty(t, f)

	assert.Len(t, f.notifier.Published, 1, "repeated identical error must notify once")
}

// エラーメッセージが変わったら（新しい失敗の様相なら）改めて通知しなければならない
func TestChangedErrorNotifiesAgain(t *testing.T) {
	f := newFixture(t)
	f.pinnedStoppedGroup("dev-db", rdsInstance("dev-db"))
	f.rds.DescribeErr = fmt.Errorf("first failure")

	runEmpty(t, f)

	f.rds.DescribeErr = fmt.Errorf("second, different failure")
	runEmpty(t, f)

	assert.Len(t, f.notifier.Published, 2, "changed error must notify again")
}

// エラー状態のリソースが収束したら、「復旧」通知が 1 度だけ飛び、last_error が消える
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

// performAction の default 節は Run 経由では到達しない（model.DecideAction は "stop"・"start"・"" しか返さない）
// それでも、将来 model.Action に値が増えたときに、未知のアクションを黙って Target へ送らないための実コードである
func TestPerformActionRejectsUnknownAction(t *testing.T) {
	tgt := porttest.NewTarget(model.TypeRdsInstance)
	err := performAction(context.Background(), model.Resource{}, "pause", tgt)
	assert.ErrorContains(t, err, `unknown action "pause"`)
	assert.Empty(t, tgt.Stopped, "the unknown-action branch must not call Stop")
	assert.Empty(t, tgt.Started, "the unknown-action branch must not call Start")
}

// start/stop のアクション成功が直前のエラーを解消した場合、「復旧」通知は別途送らない
// アクション自身の通知がすでに復旧を伝えているためである
// TestRecoveryNotifiesOnceAndClearsError が通る収束済み・アクションなしの経路では、こちらは通知を送る
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

// 復旧したエラーを消す途中の PutStatus 失敗は、ログに残すだけで reconcile のエラーにも panic にもしてはならない
// 過去のエラーに関する記録の更新が失敗しただけで、リソース自体は収束しているためである
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

// アクション成功後の Publish 失敗を reconcile のエラーとして記録してはならない
// アクションは成功し、永続化も済んでいるためである
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

// アクション成功後の PutStatus 失敗はそのサイクルのエラーとして記録され、他のリソースの reconcile は続行される
// 永続化そのものが失敗した場合でも、リソース単位の隔離は保たれる
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

// エラー記録用の PutStatus まで失敗しても、Run は panic せず他のリソースの処理を続けなければならない
// その失敗はログに残すだけである
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

// disabled は override より強い停止である
// disable は override# アイテムを消さないので、pin → override → disable の順に操作すれば「disabled なグループに有効期限内の override が残っている」状態は普通に作れる
// この経路で reconciler が override を拾ってしまうと、止めたはずのグループが期限切れまで動き出す
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

// cheapskate は Stop の前に復元用の状態を先行書き込みしなくなった
// ECS の起動時 desired count とスケーリングの上下限は、保存したステータスではなくリソース自身のタグから来る
// そのため Stop の失敗（ここでは Stop がそのまま失敗する形で模した）は、記録されたエラーだけを残さなければならない
// last_action と observed_state はアクション成功後にしか書かないので、残ってはならない
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

// 動的探索による設定の継承を示す
// すでに pinned や schedule のグループのセレクタに新たにマッチしたリソースは、次の reconcile でただちに操作される
// 誰かがタグを付けただけで、グループやスケジュールの設定は一切変えていない状況にあたる
// これがグループ優先の設計における中心的な約束である
func TestNewlyDiscoveredResourceInheritsGroupsExistingPinOnNextReconcile(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModePinned, model.DesiredStopped)
	f.discoverer.ByTagValue["dev"] = []model.Resource{rdsInstance("dev-db")}
	f.rds.Observations["dev-db"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)
	require.Len(t, f.rds.Stopped, 1, "first resource acted on")

	// 2 つめのリソースが、すでに pinned な同じグループのセレクタにマッチし始める
	// タグを付けたばかりの状況にあたり、グループ設定は何も変えていない
	f.discoverer.ByTagValue["dev"] = []model.Resource{rdsInstance("dev-db"), ecsService("dev-cluster/api")}
	f.ecs.Observations["dev-cluster/api"] = model.Observation{State: model.StateRunning}

	runEmpty(t, f)

	assert.Equal(t, []string{"dev-cluster/api"}, f.ecs.Stopped, "newly discovered resource must inherit the group's existing pin and be acted on immediately")
}

// グループ単位の失敗（ここでは不正な cron）は、リソースごとに散らさず "group#<name>" のステータスへ 1 度だけ記録される
// resolveGroup が失敗した時点で Discover は呼ばれもしないためである
// 通知の重複排除はグループ単位でも同じように効く
func TestGroupLevelErrorRecordedOnceNotPerResource(t *testing.T) {
	f := newFixture(t)
	f.seedGroup("dev", model.ModeSchedule, "") // start/stop の cron がないので schedule.ResolveDesired が失敗する

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

// あるグループ配下のアイテム（ここでは override）が壊れていても、他のグループの reconcile を止めてはならない
// 壊れたグループについては desired が確定できないので、推測して操作するのではなく必ず何もしない
// 誤った向きへ倒すと、止めるべきでないものを止める・起こすべきでないものを起こすことになる
// 失敗はグループ単位のエラーと同じ経路（status#group#<name>）へ記録し、オペレータに届ける
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

// 孤立データ（対応する group# アイテムのない override など）は警告付きでスキップしなければならない
// これは削除の途中で落ちた場合に生じる
// 落ちることも、黙って何かの既定値へ誤って解決することも許されない
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

// 既知かどうかを問わず、どのイベントソースでも全体 reconcile に落ちる
// そもそも落ちる元となる絞り込み経路がもう存在しない
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
// 取り損ねたグループは、黙って二重管理せずリソース単位のエラーを受け取る
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
// 共有アイテムへ書くと、そのリソースを所有するグループの clearRecoveredError と同じ 1 件を毎サイクル奪い合うことになる
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

// transitioning_since の記録・消去はどちらもベストエフォートである
// これは監査のための情報であって収束の判断には使わないので、書けなかったことを理由にそのサイクルをエラーにしたり、他のリソースの処理を止めたりしてはならない
// 黙って捨てるのではなく、なぜ残らなかったのかは必ずログに出す
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

// 探索できたリソースの種別に対応する Target が結線されていないのは、結線側の不備である
// 黙って飛ばすと、そのリソースだけが永久に収束しないまま誰にも気づかれない
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
// 次のサイクルは last_error が空なので復旧通知を再送しない（重複排除の代償として受け入れる）
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

// 通知の重複排除は「前回の last_error と同じか」で決まるので、前回を読めなければ判断できない
// 読めなかったことを黙って「同じ」と扱うと、初報すら握りつぶしかねない
// 読めない側へ倒して通知し、読めなかった事実はログに残す
func TestErrorReportingNotifiesWhenPreviousStatusCannotBeRead(t *testing.T) {
	f := newFixture(t)
	var logBuf bytes.Buffer
	f.deps.Log = slog.New(slog.NewTextHandler(&logBuf, nil))
	f.seedGroup("dev", model.ModeSchedule, "") // start/stop の cron がないので resolveGroup が失敗する
	f.db.FailOn("get", "status#group#dev", fmt.Errorf("dynamodb unavailable"))

	summary := runEmpty(t, f)

	require.Len(t, summary.Errors, 1)
	assert.Contains(t, logBuf.String(), "status-read-failed")
	assert.Len(t, f.notifier.Published, 1, "重複排除の判断がつかないなら通知する側へ倒す")
}

// 起動そのものが成立しない失敗（イベントが読めない、テーブルが読めない）は、空の Summary として黙って成功するのではなく Run のエラーにする
// Lambda の呼び出しを失敗させ、再試行とアラームに載せるためである
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
