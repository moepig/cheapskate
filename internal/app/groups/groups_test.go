package groups

import (
	"context"
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

func newFixture(t *testing.T) (*mocks.DynaStore, *state.Store) {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	return db, state.New(api, "t")
}

var now = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

var devSelector = model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}}

// pin -> schedule -> pin の順に遷移しても、cron のフィールドは保持されなければならない
// Pin と Schedule はいずれも同一のグループアイテムを read-modify-write するためである
func TestPinScheduleRoundTripPreservesCronFields(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	group := "dev"

	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	_, err = Schedule(ctx, s, group, model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 18 * * *"})
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))

	got, err := s.GetGroup(ctx, group)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "0 9 * * *", got.StartCron, "pin must preserve cron fields")
	assert.Equal(t, "0 18 * * *", got.StopCron, "pin must preserve cron fields")

	_, err = Schedule(ctx, s, group, model.ScheduleSpec{StartCron: "0 8 * * *", StopCron: "0 20 * * *"})
	require.NoError(t, err)
	got, err = s.GetGroup(ctx, group)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "0 8 * * *", got.StartCron, "schedule must still update cron fields")
	assert.Equal(t, "0 20 * * *", got.StopCron, "schedule must still update cron fields")
}

func TestUnpinRestoresScheduleWhenCronsPresent(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	group := "dev"

	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	_, err = Schedule(ctx, s, group, model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 18 * * *"})
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))

	item, err := Unpin(ctx, s, group)
	require.NoError(t, err)
	assert.Equal(t, model.ModeSchedule, item.Mode)
	assert.Equal(t, "0 9 * * *", item.StartCron, "unpin must not lose cron fields")
}

func TestUnpinFallsBackToDisabledWithoutCrons(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	group := "dev"

	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))

	item, err := Unpin(ctx, s, group)
	require.NoError(t, err)
	assert.Equal(t, model.ModeDisabled, item.Mode)
}

func TestUnpinRejectsUnknownGroup(t *testing.T) {
	_, s := newFixture(t)
	_, err := Unpin(context.Background(), s, "ghost")
	require.Error(t, err)
}

func TestSetSelectorCreatesGroupWhenAbsent(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()

	created, err := SetSelector(ctx, s, "dev", devSelector)
	require.NoError(t, err)
	assert.True(t, created, "first set-selector must create the group")

	group, err := s.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, group)
	assert.Equal(t, model.ModeDisabled, group.Mode)
	assert.Equal(t, "env", group.TagKey)
	assert.Equal(t, "dev", group.TagValue)
	assert.Equal(t, []model.ResourceType{model.TypeRdsInstance}, group.Types)
}

// 既存グループに対する set-selector の再実行は、作成として報告してはならない
// mode、desired、cron は保持し、read-modify-write による変更はセレクタのフィールドに限る
func TestSetSelectorOnExistingGroupPreservesModeAndCrons(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	group := "dev"
	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	_, err = Schedule(ctx, s, group, model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 18 * * *"})
	require.NoError(t, err)

	newSel := model.Selector{TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance, model.TypeEcsService}}
	created, err := SetSelector(ctx, s, group, newSel)
	require.NoError(t, err)
	assert.False(t, created, "set-selector on an existing group must not report creation")

	got, err := s.GetGroup(ctx, group)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, model.ModeSchedule, got.Mode, "existing mode must be preserved")
	assert.Equal(t, "0 9 * * *", got.StartCron, "existing crons must be preserved")
	assert.ElementsMatch(t, []model.ResourceType{model.TypeRdsInstance, model.TypeEcsService}, got.Types, "selector must be updated")
}

func TestSetSelectorValidatesSelector(t *testing.T) {
	_, s := newFixture(t)
	_, err := SetSelector(context.Background(), s, "dev", model.Selector{TagKey: "env"})
	require.Error(t, err, "missing tag value must be rejected")

	_, err = SetSelector(context.Background(), s, "dev", model.Selector{TagKey: "env", TagValue: "dev"})
	require.Error(t, err, "missing types must be rejected")

	_, err = SetSelector(context.Background(), s, "-bad", devSelector)
	require.Error(t, err, "invalid group name must be rejected")
}

// 未登録のグループへの override は効果がないため、拒否する
func TestSetOverrideRejectsUnknownGroup(t *testing.T) {
	_, s := newFixture(t)
	_, err := SetOverride(context.Background(), s, "ghost", model.DesiredRunning, time.Hour, now)
	require.Error(t, err, "want error for unknown group")
}

// disabled は override より優先度の高い停止である (reconciler は override の評価前に disabled のグループをスキップする)
// 登録しても効果を持たないため、SetOverride は拒否しなければならない
func TestSetOverrideRejectsDisabled(t *testing.T) {
	f, s := newFixture(t)
	ctx := context.Background()
	group := "dev"
	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))
	require.NoError(t, Disable(ctx, s, group))

	_, err = SetOverride(ctx, s, group, model.DesiredRunning, time.Hour, now)
	require.Error(t, err, "want error overriding a disabled group")
	assert.Nil(t, f.Item("override#"+group), "rejected override must not be written")
}

func TestSetOverrideAllowedWhenScheduledOrPinned(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	group := "dev"
	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))

	_, err = SetOverride(ctx, s, group, model.DesiredRunning, time.Hour, now)
	require.NoError(t, err, "override on a pinned group must be allowed")
}

func TestPinScheduleDisableRejectUnknownGroup(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	require.Error(t, Pin(ctx, s, "ghost", model.DesiredStopped))
	_, err := Schedule(ctx, s, "ghost", model.ScheduleSpec{StartCron: "0 9 * * *"})
	require.Error(t, err)
	require.Error(t, Disable(ctx, s, "ghost"))
}

// グループ名を受け取る入口はすべて model.ValidGroupName を通さなければならない
// 検証を行う関数と行わない関数が混在すると、検証済みという前提が呼び出し側 (CLI と web console) の実装に依存する
// 名前は pk へそのまま入るため、区切り文字を含む名前をここで拒否することが不変条件である
func TestNameTakingEntryPointsRejectInvalidNames(t *testing.T) {
	bad := "dev#1/2"
	ops := map[string]func(context.Context, Store) error{
		"SetSelector": func(ctx context.Context, s Store) error { _, err := SetSelector(ctx, s, bad, devSelector); return err },
		"Pin":         func(ctx context.Context, s Store) error { return Pin(ctx, s, bad, model.DesiredStopped) },
		"Unpin":       func(ctx context.Context, s Store) error { _, err := Unpin(ctx, s, bad); return err },
		"Schedule": func(ctx context.Context, s Store) error {
			_, err := Schedule(ctx, s, bad, model.ScheduleSpec{StartCron: "0 9 * * *"})
			return err
		},
		"Disable": func(ctx context.Context, s Store) error { return Disable(ctx, s, bad) },
		"SetOverride": func(ctx context.Context, s Store) error {
			_, err := SetOverride(ctx, s, bad, model.DesiredRunning, time.Hour, now)
			return err
		},
		"ClearOverride": func(ctx context.Context, s Store) error { return ClearOverride(ctx, s, bad) },
		"RemoveGroup":   func(ctx context.Context, s Store) error { return RemoveGroup(ctx, s, bad) },
	}
	pks := []string{"group#" + bad, "override#" + bad, "status#group#" + bad}
	for name, op := range ops {
		t.Run(name, func(t *testing.T) {
			f, s := newFixture(t)
			// その名前のアイテムが手作業により存在する場合も、書き換えと削除のいずれも行ってはならない
			for _, pk := range pks {
				f.Seed(map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: pk}, "mode": &types.AttributeValueMemberS{Value: string(model.ModeDisabled)}})
			}

			err := op(context.Background(), s)

			require.Error(t, err)
			assert.Contains(t, err.Error(), bad, "どの名前が悪いのか分かる文言でなければならない")
			for _, pk := range pks {
				assert.NotNilf(t, f.Item(pk), "名前を拒んだ操作は %s に触れてはならない", pk)
			}
		})
	}
}

func TestRemoveGroupDeletesOverrideStatusAndGroup(t *testing.T) {
	f, s := newFixture(t)
	ctx := context.Background()
	group := "dev"
	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))
	_, err = SetOverride(ctx, s, group, model.DesiredRunning, time.Hour, now)
	require.NoError(t, err)
	require.NoError(t, s.UpdateStatus(ctx, model.GroupStatusID(group), state.StatusPatch{LastError: state.Set("boom")}))

	require.NoError(t, RemoveGroup(ctx, s, group))

	for _, pk := range []string{"group#" + group, "override#" + group, "status#group#" + group} {
		assert.Nilf(t, f.Item(pk), "%s not deleted", pk)
	}
}

// RemoveGroup は override、ステータス、グループアイテム本体の順に削除する
// この順序では、途中で失敗してもグループアイテムが残り、再試行のためにグループへ到達できる (RemoveGroup のコメントを参照)
// 最初の削除が失敗した場合、他のすべてが残存しなければならない
func TestRemoveGroupStopsOnOverrideDeleteFailure(t *testing.T) {
	f, s := newFixture(t)
	ctx := context.Background()
	group := "dev"
	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))
	_, err = SetOverride(ctx, s, group, model.DesiredRunning, time.Hour, now)
	require.NoError(t, err)
	require.NoError(t, s.UpdateStatus(ctx, model.GroupStatusID(group), state.StatusPatch{LastError: state.Set("boom")}))

	f.FailOn("delete", "override#"+group, assert.AnError)
	require.ErrorIs(t, RemoveGroup(ctx, s, group), assert.AnError)

	assert.NotNilf(t, f.Item("override#"+group), "override must survive when its own delete fails")
	assert.NotNilf(t, f.Item("status#group#"+group), "status must survive a failure before it is reached")
	assert.NotNilf(t, f.Item("group#"+group), "group item must survive a failure before it is reached")
}

// 2 番目の削除であるステータスが失敗した場合も、グループアイテムは残存しなければならない
// 先行する override が削除済みである場合も同様である
func TestRemoveGroupStopsOnStatusDeleteFailure(t *testing.T) {
	f, s := newFixture(t)
	ctx := context.Background()
	group := "dev"
	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))
	_, err = SetOverride(ctx, s, group, model.DesiredRunning, time.Hour, now)
	require.NoError(t, err)
	require.NoError(t, s.UpdateStatus(ctx, model.GroupStatusID(group), state.StatusPatch{LastError: state.Set("boom")}))

	f.FailOn("delete", "status#group#"+group, assert.AnError)
	require.ErrorIs(t, RemoveGroup(ctx, s, group), assert.AnError)

	assert.Nilf(t, f.Item("override#"+group), "override delete must have already gone through")
	assert.NotNilf(t, f.Item("status#group#"+group), "status must survive when its own delete fails")
	assert.NotNilf(t, f.Item("group#"+group), "group item must survive a failure before it is reached")
}

// あるグループの override が壊れている場合も、List は処理を継続し、その行のエラーを報告しなければならない
func TestListSurfacesPerRowErrorWithoutAbortingOthers(t *testing.T) {
	f, s := newFixture(t)
	ctx := context.Background()
	_, err := SetSelector(ctx, s, "broken", devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, "broken", model.DesiredStopped))
	f.Seed(map[string]types.AttributeValue{
		"pk":         &types.AttributeValueMemberS{Value: "override#broken"},
		"desired":    &types.AttributeValueMemberS{Value: "not-a-valid-state"},
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	_, err = SetSelector(ctx, s, "fine", devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, "fine", model.DesiredStopped))

	rows, err := List(ctx, s, now)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byName := map[string]GroupRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	assert.Error(t, byName["broken"].Err, "malformed row must carry its error")
	assert.NoError(t, byName["fine"].Err, "unrelated row must be unaffected")
}

// GetDetail はグループのセレクタに現在一致する全リソースを解決し、そのステータスと結合しなければならない
// CLI の `show` とコンソールのグループページは、グループの設定アイテムではなく、現在の管理対象リソースを必要とするためである
func TestGetDetailResolvesResourcesWithStatus(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	group := "dev"
	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))
	require.NoError(t, s.UpdateStatus(ctx, "rds-instance#db", state.StatusPatch{ObservedState: state.Set(model.StateStopped)}))

	resources := []model.Resource{{Type: model.TypeRdsInstance, Ref: "db", ARN: "arn:aws:rds:...:db:db"}}
	d := &porttest.Discoverer{Resources: resources}

	detail, err := GetDetail(ctx, s, d, nil, group, now)
	require.NoError(t, err)
	require.Len(t, detail.Resources, 1)
	assert.Equal(t, "db", detail.Resources[0].Resource.Ref)
	assert.Equal(t, model.StateStopped, detail.Resources[0].Status.ObservedState)
	assert.Nil(t, detail.Resources[0].Live, "no port.Describer wired for rds-instance in this test, so Live must stay nil")
	assert.NoError(t, detail.DiscoverErr)
	assert.Equal(t, []model.Selector{devSelector}, d.Selectors, "resources must be discovered with the group's own selector")
}

// リソース種別に port.Describer が結線されている場合は必ず問い合わせ、その Observation を ResourceRow.Live へ格納しなければならない
// これにより webconsole と CLI は、最後に記録した reconcile のステータスではなく、現在の状態を表示できる
func TestGetDetailPopulatesLiveStateWhenDescriberWired(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	group := "dev"
	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))

	resources := []model.Resource{{Type: model.TypeRdsInstance, Ref: "db", ARN: "arn:aws:rds:...:db:db"}}
	d := &porttest.Discoverer{Resources: resources}

	obs := model.Observation{State: model.StateRunning, Detail: "available"}
	describers := map[model.ResourceType]port.Describer{model.TypeRdsInstance: porttest.Describer{Obs: obs}}

	detail, err := GetDetail(ctx, s, d, describers, group, now)
	require.NoError(t, err)
	require.Len(t, detail.Resources, 1)
	require.NotNil(t, detail.Resources[0].Live)
	assert.Equal(t, obs, *detail.Resources[0].Live)
	assert.NoError(t, detail.Resources[0].LiveErr)
}

// Describe の失敗は、エラーではなくデータとして ResourceRow.LiveErr に現れなければならない
// これによりリソース 1 件の Describe 権限の不備は、グループのページやコマンド全体を失敗させない
// Discover の失敗を GroupDetail.DiscoverErr とするのと同じ扱いである
func TestGetDetailReturnsLiveErrAsDataOnDescribeFailure(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	group := "dev"
	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))

	resources := []model.Resource{{Type: model.TypeRdsInstance, Ref: "db", ARN: "arn:aws:rds:...:db:db"}}
	d := &porttest.Discoverer{Resources: resources}

	describers := map[model.ResourceType]port.Describer{model.TypeRdsInstance: porttest.Describer{Err: assert.AnError}}

	detail, err := GetDetail(ctx, s, d, describers, group, now)
	require.NoError(t, err, "a Describe failure must not fail GetDetail itself")
	require.Len(t, detail.Resources, 1)
	assert.Nil(t, detail.Resources[0].Live)
	assert.Error(t, detail.Resources[0].LiveErr)
}

// Discover の失敗は関数のエラーではなく、GroupDetail.DiscoverErr にデータとして現れる
// 呼び出し側である CLI と webconsole が、失敗せずにグループの設定を表示できなければならないためである
func TestGetDetailReturnsDiscoverErrAsData(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	group := "dev"
	_, err := SetSelector(ctx, s, group, devSelector)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, group, model.DesiredStopped))

	d := &porttest.Discoverer{Err: assert.AnError}

	detail, err := GetDetail(ctx, s, d, nil, group, now)
	require.NoError(t, err, "discover failure must not fail GetDetail itself")
	assert.Error(t, detail.DiscoverErr)
	assert.Empty(t, detail.Resources)
}

// disabled のグループ、およびセレクタが未設定のグループでは、Discover を呼んではならない
func TestGetDetailSkipsDiscoveryWhenSelectorEmpty(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	require.NoError(t, s.PutGroup(ctx, model.GroupSpec{Name: "dev", Mode: model.ModeDisabled}))

	d := &porttest.Discoverer{}

	detail, err := GetDetail(ctx, s, d, nil, "dev", now)
	require.NoError(t, err)
	assert.Empty(t, detail.Resources)
	assert.NoError(t, detail.DiscoverErr)
	assert.Zero(t, d.Calls(), "a disabled group must never call Discover")
}

func TestGetDetailRejectsUnknownGroup(t *testing.T) {
	_, s := newFixture(t)
	d := &porttest.Discoverer{}
	_, err := GetDetail(context.Background(), s, d, nil, "ghost", now)
	require.Error(t, err)
	assert.Zero(t, d.Calls(), "an unknown group must be rejected before any discovery")
}

// 変更操作は、reconciler が従えない設定を保存してはならない
// 検証は書き込みより前に行うため、拒否された場合のテーブルは変更前の状態を保つ
// 規則自体は model.GroupSpec の遷移が持ち、本テストが確かめるのは、その拒否がストアへの到達前に働くことである
func TestMutationsRejectUnusableConfigWithoutWriting(t *testing.T) {
	ctx := context.Background()
	group := "dev"

	cases := map[string]func(s Store) error{
		"invalid start cron": func(s Store) error {
			_, err := Schedule(ctx, s, group, model.ScheduleSpec{StartCron: "every morning"})
			return err
		},
		"invalid stop cron": func(s Store) error {
			_, err := Schedule(ctx, s, group, model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "nightly"})
			return err
		},
		"unknown timezone": func(s Store) error {
			_, err := Schedule(ctx, s, group, model.ScheduleSpec{StartCron: "0 9 * * *", Timezone: "Mars/Olympus"})
			return err
		},
		"no cron at all": func(s Store) error {
			_, err := Schedule(ctx, s, group, model.ScheduleSpec{Timezone: "Asia/Tokyo"})
			return err
		},
		"desired that is neither running nor stopped": func(s Store) error {
			return Pin(ctx, s, group, "paused")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			db, s := newFixture(t)
			_, err := SetSelector(ctx, s, group, devSelector)
			require.NoError(t, err)
			before := db.Item("group#" + group)
			require.NotNil(t, before)

			require.Error(t, mutate(s))

			assert.Equal(t, before, db.Item("group#"+group), "拒否された変更はテーブルを触ってはならない")
		})
	}
}

// disabled は、設定の内容によらず適用できなければならない
// 設定が壊れているグループの管理を停止する唯一の手段であるためである
func TestDisableWorksOnAGroupWhoseConfigIsUnusable(t *testing.T) {
	db, s := newFixture(t)
	ctx := context.Background()
	// reconciler が従えない設定を、遷移を経由せず直接投入する
	require.NoError(t, s.PutGroup(ctx, model.GroupSpec{
		Name: "broken", Mode: model.ModeSchedule, StartCron: "every morning",
		TagKey: "env", TagValue: "broken", Types: []model.ResourceType{model.TypeRdsInstance},
	}))
	_, perr := model.ParseGroup(model.GroupSpec{
		Name: "broken", Mode: model.ModeSchedule, StartCron: "every morning",
		TagKey: "env", TagValue: "broken", Types: []model.ResourceType{model.TypeRdsInstance},
	})
	require.Error(t, perr, "この設定は ParseGroup が拒否する (前提の確認)")

	require.NoError(t, Disable(ctx, s, "broken"))

	assert.Equal(t, string(model.ModeDisabled), db.Item("group#broken")["mode"].(*types.AttributeValueMemberS).Value)
}

// mode 属性を持たないアイテムを、reconciler は disabled として扱う
// override を受け付けた場合、操作は成功するが効果を持たない
func TestSetOverrideRejectsGroupWithNoModeAttribute(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	require.NoError(t, s.PutGroup(ctx, model.GroupSpec{
		Name: "raw", TagKey: "env", TagValue: "raw", Types: []model.ResourceType{model.TypeRdsInstance},
	}))

	_, err := SetOverride(ctx, s, "raw", model.DesiredRunning, time.Hour, time.Now())
	require.Error(t, err, "mode 未設定は disabled と同じであり、override は反映されない")
	assert.Contains(t, err.Error(), "disabled")
}
