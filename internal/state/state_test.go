package state

import (
	"context"
	"maps"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/core/model"
	"cheapskate/internal/state/mocks"
)

func s[T ~string](v T) types.AttributeValue { return &types.AttributeValueMemberS{Value: string(v)} }

func newFixture(t *testing.T) (*mocks.DynaStore, *Store) {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	return db, New(api, "t")
}

func seedGroup(db *mocks.DynaStore, name string, mode model.Mode, desired model.DesiredState) {
	db.Seed(map[string]types.AttributeValue{
		"pk": s(groupKey(name)), "mode": s(string(mode)), "desired": s(string(desired)),
	})
}

func seedGroupStatus(db *mocks.DynaStore, name string, attrs map[string]types.AttributeValue) {
	item := map[string]types.AttributeValue{"pk": s(groupStatusKey(name))}
	maps.Copy(item, attrs)
	db.Seed(item)
}

func seedStatus(db *mocks.DynaStore, resourceID string, attrs map[string]types.AttributeValue) {
	item := map[string]types.AttributeValue{"pk": s(statusKey(resourceID))}
	maps.Copy(item, attrs)
	db.Seed(item)
}

// ScanAll は LastEvaluatedKey を用いて Scan をページ送りしなければならない
// 実テーブルの Scan は 1 回あたり 1MB で打ち切られるためである
func TestScanAllPagesThroughScan(t *testing.T) {
	db, st := newFixture(t)
	db.SetScanPageSize(1)
	seedGroup(db, "a", model.ModePinned, model.DesiredStopped)
	seedGroup(db, "b", model.ModePinned, model.DesiredStopped)
	seedGroup(db, "c", model.ModePinned, model.DesiredStopped)

	res, err := st.ScanAll(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Len(t, res.Groups, 3)
}

func TestScanAllJoinsGroupOverrideGroupStatus(t *testing.T) {
	db, st := newFixture(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	seedGroup(db, "dev", model.ModePinned, model.DesiredStopped)
	db.Seed(map[string]types.AttributeValue{
		"pk": s(overrideKey("dev")), "desired": s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	seedGroupStatus(db, "dev", map[string]types.AttributeValue{"last_error": s("discover: access denied")})

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	r := res.Groups[0]
	assert.True(t, r.HasGroup)
	require.NotNil(t, r.Override)
	assert.Equal(t, model.DesiredRunning, r.Override.Desired)
	assert.Equal(t, "discover: access denied", r.Status.LastError)
	assert.NoError(t, r.Err)
}

// リソース単位の平坦なステータスマップは resource_id をキーとし、グループとは独立に返る
// store はリソースの所属グループを判定できない (動的な探索を要する)
// したがって結合は呼び出し側である internal/app/groups が行う
func TestScanAllReturnsFlatPerResourceStatuses(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	seedGroup(db, "dev", model.ModeSchedule, "")
	seedStatus(db, "rds-instance#a", map[string]types.AttributeValue{"last_action": s("stop")})
	seedStatus(db, "ecs-service#dev-cluster/api", map[string]types.AttributeValue{"last_action": s("start")})

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	assert.True(t, res.Groups[0].HasGroup)
	require.Contains(t, res.Statuses, "rds-instance#a")
	assert.Equal(t, model.ActionStop, res.Statuses["rds-instance#a"].LastAction)
	require.Contains(t, res.Statuses, "ecs-service#dev-cluster/api")
	assert.Equal(t, model.ActionStart, res.Statuses["ecs-service#dev-cluster/api"].LastAction)
}

// あるグループの override が壊れている場合も、他のグループを一覧から除外してはならない
// scan を中断せず、その行の Err として現れなければならない
func TestScanAllRecordsPerRowErrorForMalformedOverride(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	seedGroup(db, "broken", model.ModePinned, model.DesiredStopped)
	db.Seed(map[string]types.AttributeValue{
		"pk": s(overrideKey("broken")), "desired": s("not-a-valid-state"),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	seedGroup(db, "fine", model.ModePinned, model.DesiredStopped)

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 2)
	byName := map[string]GroupRow{}
	for _, r := range res.Groups {
		byName[r.Name] = r
	}
	assert.Error(t, byName["broken"].Err, "malformed override must set Err on its row")
	assert.True(t, byName["broken"].HasGroup, "group must still be joined despite the bad override")
	assert.NoError(t, byName["fine"].Err, "unrelated row must be unaffected")
}

// unmarshal できない status#group#<name> アイテムは、他グループの scan を中断させてはならない
// そのグループの行の Err として現れなければならない
func TestScanAllRecordsPerRowErrorForMalformedGroupStatus(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	seedGroup(db, "broken", model.ModePinned, model.DesiredStopped)
	db.Seed(map[string]types.AttributeValue{
		"pk":          s(groupStatusKey("broken")),
		"last_error":  &types.AttributeValueMemberBOOL{Value: true}, // 文字列でなければならず、UnmarshalMap が失敗する
		"last_action": s("stop"),
	})
	seedGroup(db, "fine", model.ModePinned, model.DesiredStopped)

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	byName := map[string]GroupRow{}
	for _, r := range res.Groups {
		byName[r.Name] = r
	}
	assert.Error(t, byName["broken"].Err, "malformed group-status must set Err on its row")
	assert.NoError(t, byName["fine"].Err, "unrelated row must be unaffected")
}

// unmarshal できない group# アイテムは、他グループの scan を中断させてはならない
// その行の Err として現れ、かつ HasGroup は false のままでなければならない
// 設定を読めていない状態を、登録済みのグループとして扱う根拠が存在しないためである
// doctor はこの組み合わせを corrupt-record として報告し、孤立判定を見送る
func TestScanAllRecordsPerRowErrorForMalformedGroup(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	db.Seed(map[string]types.AttributeValue{
		"pk":   s(groupKey("broken")),
		"mode": &types.AttributeValueMemberBOOL{Value: true}, // 文字列でなければならず、UnmarshalMap が失敗する
	})
	seedGroup(db, "fine", model.ModePinned, model.DesiredStopped)

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 2)
	byName := map[string]GroupRow{}
	for _, r := range res.Groups {
		byName[r.Name] = r
	}
	assert.Error(t, byName["broken"].Err, "malformed group must set Err on its row")
	assert.False(t, byName["broken"].HasGroup, "読めなかった設定を登録済みとして扱ってはならない")
	assert.NoError(t, byName["fine"].Err, "unrelated row must be unaffected")
	assert.True(t, byName["fine"].HasGroup)
}

// pk を持たないアイテムは種別を判定できないため、行を作らずスキップする
// 手作業により投入されたアイテム、および別の用途のアイテムが scan を失敗させてはならない
func TestScanAllSkipsItemsWithoutPK(t *testing.T) {
	db, st := newFixture(t)
	db.Seed(map[string]types.AttributeValue{"note": s("hand-written row")})
	db.Seed(map[string]types.AttributeValue{"pk": &types.AttributeValueMemberN{Value: "42"}}) // pk は文字列でなければならない
	seedGroup(db, "fine", model.ModePinned, model.DesiredStopped)

	res, err := st.ScanAll(context.Background(), time.Now())
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	assert.Equal(t, "fine", res.Groups[0].Name)
	assert.Empty(t, res.Statuses)
}

// 壊れたリソース単位の status アイテムには、エラーを対応づける行が存在しない
// リソースとグループの対応づけには、この scan ではなく動的な探索を要するためである
// したがってスキップし、他の行は影響を受けない
func TestScanAllSkipsMalformedPerResourceStatus(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	db.Seed(map[string]types.AttributeValue{
		"pk":          s(statusKey("rds-instance#a")),
		"last_action": &types.AttributeValueMemberBOOL{Value: true}, // 文字列でなければならず、UnmarshalMap が失敗する
	})
	seedGroup(db, "fine", model.ModePinned, model.DesiredStopped)

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	assert.NoError(t, res.Groups[0].Err)
	assert.NotContains(t, res.Statuses, "rds-instance#a")
}

func TestScanAllExpiredOverrideIsIgnored(t *testing.T) {
	db, st := newFixture(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	seedGroup(db, "dev", model.ModePinned, model.DesiredStopped)
	db.Seed(map[string]types.AttributeValue{
		"pk": s(overrideKey("dev")), "desired": s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "1"}, // はるか過去
	})

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	assert.Nil(t, res.Groups[0].Override, "expired override must be ignored")
}

func TestScanAllSkipsOrphanedOverrideWithoutGroup(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	db.Seed(map[string]types.AttributeValue{
		"pk": s(overrideKey("ghost")), "desired": s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	assert.False(t, res.Groups[0].HasGroup, "orphaned override must be reported without HasGroup")
}

func TestScanAllReportsOrphanedGroupStatusWithoutGroup(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	seedGroupStatus(db, "ghost", map[string]types.AttributeValue{"last_error": s("boom")})

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	assert.False(t, res.Groups[0].HasGroup)
	assert.Equal(t, "boom", res.Groups[0].Status.LastError)
}

func TestGetPutGroup(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	got, err := st.GetGroup(ctx, "dev")
	require.NoError(t, err)
	assert.Nil(t, got)

	require.NoError(t, st.PutGroup(ctx, model.GroupSpec{Name: "dev", Mode: model.ModeDisabled}))
	got, err = st.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, model.ModeDisabled, got.Mode)
}

func TestGetOverrideByGroupName(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

	got, err := st.GetOverride(ctx, "dev", now)
	require.NoError(t, err)
	assert.Nil(t, got)

	require.NoError(t, st.PutOverride(ctx, "dev", model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(time.Hour).Unix()}))
	got, err = st.GetOverride(ctx, "dev", now)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, model.DesiredRunning, got.Desired)
}

func TestGetOverrideEnforcesExpiryAndValidatesDesired(t *testing.T) {
	db, st := newFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

	db.Seed(map[string]types.AttributeValue{
		"pk": s(overrideKey("expired")), "desired": s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "0"},
	})
	got, err := st.GetOverride(ctx, "expired", now)
	require.NoError(t, err)
	assert.Nil(t, got, "an override past its expiry must read back as absent")

	db.Seed(map[string]types.AttributeValue{
		"pk": s(overrideKey("broken")), "desired": s("not-a-valid-state"),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	_, err = st.GetOverride(ctx, "broken", now)
	assert.Error(t, err, "an invalid desired value must be rejected rather than silently trusted")
}

func TestPutGroupPropagatesStoreError(t *testing.T) {
	db, st := newFixture(t)
	db.FailOn("put", "group#dev", assert.AnError)
	err := st.PutGroup(context.Background(), model.GroupSpec{Name: "dev", Mode: model.ModeDisabled})
	assert.ErrorIs(t, err, assert.AnError)
}

// 属性を 1 つも設定していない StatusPatch では、UpdateItem を呼ばない
// SET 句が空の場合、式が不正となり UpdateItem が失敗するためである
// 定常状態における書き込みの抑制にも対応する
func TestUpdateStatusWithEmptyPatchSkipsUpdate(t *testing.T) {
	db, st := newFixture(t)
	db.FailOn("update", statusKey("rds-instance#a"), assert.AnError) // 呼ばれたら気づけるようにしておく

	err := st.UpdateStatus(context.Background(), "rds-instance#a", StatusPatch{})

	require.NoError(t, err)
	assert.Nil(t, db.Item(statusKey("rds-instance#a")), "何も書くものがなければアイテムを作ってはならない")
}

// 空文字を指すポインタは属性の削除を表し、nil が表す変更なしとは異なる
// clearRecoveredError がエラーを削除する際に用いる経路であり、ポインタとする根拠に該当する
func TestUpdateStatusDistinguishesClearFromUntouched(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	require.NoError(t, st.UpdateStatus(ctx, "rds-instance#a", StatusPatch{
		LastError: Set("boom"), LastAction: Set(model.ActionStop),
	}))

	require.NoError(t, st.UpdateStatus(ctx, "rds-instance#a", StatusPatch{LastError: Set("")}))

	got, err := st.GetStatus(ctx, "rds-instance#a")
	require.NoError(t, err)
	assert.Empty(t, got.LastError, "空文字を指すポインタは属性を消す")
	assert.Equal(t, model.ActionStop, got.LastAction, "パッチに含めなかった属性は触らない")
}

// 削除は pk の組み立てのみを行うが、種別ごとの接頭辞が正しくない場合、対象のアイテムが残り、対象外のアイテムが削除される
// 4 種類がそれぞれ対象のアイテムのみを削除することを確かめる
func TestDeletesTargetTheRightItem(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		seededPK string
		remove   func(*Store) error
	}{
		"group":        {groupKey("dev"), func(s *Store) error { return s.DeleteGroup(ctx, "dev") }},
		"override":     {overrideKey("dev"), func(s *Store) error { return s.DeleteOverride(ctx, "dev") }},
		"group status": {groupStatusKey("dev"), func(s *Store) error { return s.DeleteGroupStatus(ctx, "dev") }},
		"status":       {statusKey("rds-instance#a"), func(s *Store) error { return s.DeleteStatus(ctx, "rds-instance#a") }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db, st := newFixture(t)
			// 4 種すべてを投入し、対象のアイテムのみが削除されることを確かめる
			seedGroup(db, "dev", model.ModePinned, model.DesiredStopped)
			db.Seed(map[string]types.AttributeValue{"pk": s(overrideKey("dev")), "desired": s(model.DesiredRunning),
				"expires_at": &types.AttributeValueMemberN{Value: "9999999999"}})
			seedGroupStatus(db, "dev", map[string]types.AttributeValue{"last_error": s("boom")})
			seedStatus(db, "rds-instance#a", map[string]types.AttributeValue{"last_action": s("stop")})

			require.NoError(t, tc.remove(st))

			assert.Nil(t, db.Item(tc.seededPK), "%s must be deleted", tc.seededPK)
			for _, pk := range []string{groupKey("dev"), overrideKey("dev"), groupStatusKey("dev"), statusKey("rds-instance#a")} {
				if pk != tc.seededPK {
					assert.NotNilf(t, db.Item(pk), "%s must survive", pk)
				}
			}
		})
	}
}

// 存在しないアイテムの削除はエラーとならない
// doctor --prune の失敗時に再実行できるという前提が、この性質に依存する
func TestDeleteIsIdempotent(t *testing.T) {
	_, st := newFixture(t)
	assert.NoError(t, st.DeleteGroup(context.Background(), "never-existed"))
}

// pk を外部へ渡す唯一の経路であり、doctor が手作業による delete-item のために表示する文字列に一致する
// キー構成を本パッケージへ限定する前提の上に成立するため、items.go の接頭辞の変更時にここが不一致となってはならない
func TestPKAccessorsMatchTheStoredKeys(t *testing.T) {
	assert.Equal(t, "group#dev", GroupPK("dev"))
	assert.Equal(t, "override#dev", OverridePK("dev"))
	assert.Equal(t, "status#group#dev", GroupStatusPK("dev"))
	assert.Equal(t, "status#rds-instance#dev-db", StatusPK("rds-instance#dev-db"))

	// グループ単位のステータスは、合成リソース ID を用いた status# アイテムでなければならない
	assert.Equal(t, StatusPK(model.GroupStatusID("dev")), GroupStatusPK("dev"))
}

func TestGetPutStatusForGroupPseudoID(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	got, err := st.GetStatus(ctx, model.GroupStatusID("dev"))
	require.NoError(t, err)
	assert.Equal(t, model.Status{}, got)

	require.NoError(t, st.UpdateStatus(ctx, model.GroupStatusID("dev"), StatusPatch{LastError: Set("discover: access denied")}))
	got, err = st.GetStatus(ctx, model.GroupStatusID("dev"))
	require.NoError(t, err)
	assert.Equal(t, "discover: access denied", got.LastError)
}
