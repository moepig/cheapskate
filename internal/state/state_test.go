package state

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/backoff"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state/mocks"
)

func s[T ~string](v T) types.AttributeValue { return &types.AttributeValueMemberS{Value: string(v)} }

func seeded(k itemKey, attrs map[string]types.AttributeValue) map[string]types.AttributeValue {
	item := marshalKey(k)
	maps.Copy(item, attrs)
	return item
}

func stored(db *mocks.DynaStore, k itemKey) map[string]types.AttributeValue {
	return db.Item(k.PK, k.SK)
}

func newFixture(t *testing.T, options ...StoreOption) (*mocks.DynaStore, *Store) {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	return db, New(api, "t", options...)
}

func seedGroup(db *mocks.DynaStore, name string, mode model.Mode, desired model.DesiredState) {
	db.Seed(seeded(groupKey(name), map[string]types.AttributeValue{"mode": s(string(mode)), "desired": s(string(desired))}))
}

func seedGroupStatus(db *mocks.DynaStore, name string, attrs map[string]types.AttributeValue) {
	db.Seed(seeded(groupStatusKey(name), attrs))
}

func seedStatus(db *mocks.DynaStore, resourceID string, attrs map[string]types.AttributeValue) {
	db.Seed(seeded(statusKey(resourceID), attrs))
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

// 通常のグループ一覧は設定partitionのQueryと、グループstatusのBatchGetItemだけで構成する。
// status履歴の件数が定常読み取りへ影響しないため、Scanを呼んではならない。
func TestListGroupsDoesNotScanStatusHistory(t *testing.T) {
	db, st := newFixture(t)
	seedGroup(db, "dev", model.ModePinned, model.DesiredStopped)
	seedGroupStatus(db, "dev", map[string]types.AttributeValue{"last_error": s("boom")})
	for i := range 150 {
		seedStatus(db, fmt.Sprintf("rds-instance#old-%03d", i), map[string]types.AttributeValue{"last_action": s("stop")})
	}

	rows, err := st.ListGroups(context.Background(), time.Now())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "boom", rows[0].Status.LastError)
	assert.Equal(t, 1, db.Calls("query"))
	assert.Equal(t, 1, db.Calls("batch-get"))
	assert.Zero(t, db.Calls("scan"))
}

// DynamoDBのBatchGetItemは1回に100キーまでであるため、101件のstatusを2回に分割する。
func TestGetStatusesBatchesAtOneHundredKeys(t *testing.T) {
	db, st := newFixture(t)
	ids := make([]string, 0, 101)
	for i := range 101 {
		id := fmt.Sprintf("rds-instance#db-%03d", i)
		ids = append(ids, id)
		seedStatus(db, id, map[string]types.AttributeValue{"last_action": s("stop")})
	}

	statuses, err := st.GetStatuses(context.Background(), ids)
	require.NoError(t, err)
	assert.Len(t, statuses, 101)
	assert.Equal(t, 2, db.Calls("batch-get"))
}

// UnprocessedKeys は同じキーを再要求し、処理済みの応答と結合しなければならない。
// テストでは待機時間を最小化し、再試行回数と最終結果を検証する。
func TestGetStatusesRetriesUnprocessedKeys(t *testing.T) {
	db, st := newFixture(t)
	st.batchGetBackoff = backoff.NewExponential(time.Nanosecond, time.Nanosecond)
	db.SetBatchGetUnprocessedResponses(2)
	seedStatus(db, "rds-instance#dev", map[string]types.AttributeValue{"last_action": s("stop")})

	got, err := st.GetStatuses(context.Background(), []string{"rds-instance#dev"})

	require.NoError(t, err)
	assert.Equal(t, model.ActionStop, got["rds-instance#dev"].Status.LastAction)
	assert.Equal(t, 3, db.Calls("batch-get"))
}

// BatchGetItem が上限回数まで全キーを未処理として返した場合は、空の成功結果ではなくエラーを返す。
// 呼び出し回数も上限と一致させ、上限を超える再試行を防ぐ。
func TestGetStatusesStopsAfterUnprocessedKeyRetryLimit(t *testing.T) {
	db, st := newFixture(t)
	st.batchGetBackoff = backoff.NewExponential(time.Nanosecond, time.Nanosecond)
	db.SetBatchGetUnprocessedResponses(batchGetMaxAttempts)
	seedStatus(db, "rds-instance#dev", map[string]types.AttributeValue{"last_action": s("stop")})

	got, err := st.GetStatuses(context.Background(), []string{"rds-instance#dev"})

	require.ErrorContains(t, err, "batch get left unprocessed keys after 8 attempts")
	assert.Nil(t, got)
	assert.Equal(t, batchGetMaxAttempts, db.Calls("batch-get"))
}

// 再試行の待機中に context が終了した場合は、次の BatchGetItem を呼ばずに終了理由を返す。
func TestGetStatusesHonorsCanceledContextWhileWaitingToRetry(t *testing.T) {
	db, st := newFixture(t)
	st.batchGetBackoff = backoff.NewExponential(time.Hour, time.Hour)
	db.SetBatchGetUnprocessedResponses(2)
	seedStatus(db, "rds-instance#dev", map[string]types.AttributeValue{"last_action": s("stop")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := st.GetStatuses(ctx, []string{"rds-instance#dev"})

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, got)
	assert.Equal(t, 1, db.Calls("batch-get"))
}

func TestScanAllJoinsGroupOverrideGroupStatus(t *testing.T) {
	db, st := newFixture(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	seedGroup(db, "dev", model.ModePinned, model.DesiredStopped)
	db.Seed(seeded(overrideKey("dev"), map[string]types.AttributeValue{
		"desired":    s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	}))
	seedGroupStatus(db, "dev", map[string]types.AttributeValue{"last_error": s("discover: access denied")})

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	r := res.Groups[0]
	assert.True(t, r.HasGroup)
	require.NotNil(t, r.Override)
	assert.Equal(t, model.DesiredRunning, r.Override.Desired)
	assert.Equal(t, "discover: access denied", r.Status.LastError)
	assert.NoError(t, r.GroupErr)
	assert.NoError(t, r.OverrideErr)
	assert.NoError(t, r.StatusErr)
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

// あるグループの override が壊れている場合も、他のグループを一覧から除外してはならない。
// Scan を中断せず、その行の OverrideErr として現れなければならない。
func TestScanAllRecordsPerRowErrorForMalformedOverride(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	seedGroup(db, "broken", model.ModePinned, model.DesiredStopped)
	db.Seed(seeded(overrideKey("broken"), map[string]types.AttributeValue{
		"desired":    s("not-a-valid-state"),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	}))
	seedGroup(db, "fine", model.ModePinned, model.DesiredStopped)

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 2)
	byName := map[string]GroupRow{}
	for _, r := range res.Groups {
		byName[r.Name] = r
	}
	assert.Error(t, byName["broken"].OverrideErr, "malformed override must set OverrideErr on its row")
	assert.True(t, byName["broken"].HasGroup, "group must still be joined despite the bad override")
	assert.NoError(t, byName["fine"].OverrideErr, "unrelated row must be unaffected")
}

// 復号できない status#group#<name> アイテムは、他グループの Scan を中断させてはならない。
// そのグループの行の StatusErr として現れなければならない。
func TestScanAllRecordsPerRowErrorForMalformedGroupStatus(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	seedGroup(db, "broken", model.ModePinned, model.DesiredStopped)
	db.Seed(seeded(groupStatusKey("broken"), map[string]types.AttributeValue{
		"last_error":  &types.AttributeValueMemberBOOL{Value: true}, // 文字列でなければならず、UnmarshalMap が失敗する
		"last_action": s("stop"),
	}))
	seedGroup(db, "fine", model.ModePinned, model.DesiredStopped)

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	byName := map[string]GroupRow{}
	for _, r := range res.Groups {
		byName[r.Name] = r
	}
	assert.Error(t, byName["broken"].StatusErr, "malformed group-status must set StatusErr on its row")
	assert.NoError(t, byName["broken"].GroupErr)
	assert.NoError(t, byName["broken"].OverrideErr)
	assert.NoError(t, byName["fine"].StatusErr, "unrelated row must be unaffected")
}

// 復号できない group# アイテムは、他グループの Scan を中断させてはならない。
// その行の GroupErr として現れ、かつ HasGroup は false のままでなければならない。
// 設定を読めていない状態を、登録済みのグループとして扱う根拠が存在しないためである。
// doctor はこの組み合わせを corrupt-record として報告し、孤立判定を見送る。
func TestScanAllRecordsPerRowErrorForMalformedGroup(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	db.Seed(seeded(groupKey("broken"), map[string]types.AttributeValue{
		"mode": &types.AttributeValueMemberBOOL{Value: true}, // 文字列でなければならず、UnmarshalMap が失敗する
	}))
	seedGroup(db, "fine", model.ModePinned, model.DesiredStopped)

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 2)
	byName := map[string]GroupRow{}
	for _, r := range res.Groups {
		byName[r.Name] = r
	}
	assert.Error(t, byName["broken"].GroupErr, "malformed group must set GroupErr on its row")
	assert.False(t, byName["broken"].HasGroup, "読めなかった設定を登録済みとして扱ってはならない")
	assert.NoError(t, byName["fine"].GroupErr, "unrelated row must be unaffected")
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

// 壊れたリソース単位の Status は、読めた属性と属性単位のエラーを同じレコードとして返す。
// リソースとグループの対応づけを必要とせず、キーのリソース ID により doctor が破損箇所を報告できる必要がある。
func TestScanAllReturnsMalformedPerResourceStatus(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	db.Seed(seeded(statusKey("rds-instance#a"), map[string]types.AttributeValue{
		"last_action": &types.AttributeValueMemberBOOL{Value: true}, // 文字列でなければならず、UnmarshalMap が失敗する
	}))
	seedGroup(db, "fine", model.ModePinned, model.DesiredStopped)

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	assert.NoError(t, res.Groups[0].GroupErr)
	require.Contains(t, res.Statuses, "rds-instance#a")
	record := res.Statuses["rds-instance#a"]
	assert.Error(t, record.Err)
	assert.Equal(t, []string{"last_action"}, record.CorruptAttributes)
}

func TestScanAllExpiredOverrideIsIgnored(t *testing.T) {
	db, st := newFixture(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	seedGroup(db, "dev", model.ModePinned, model.DesiredStopped)
	db.Seed(seeded(overrideKey("dev"), map[string]types.AttributeValue{
		"desired":    s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "1"}, // はるか過去
	}))

	res, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, res.Groups, 1)
	assert.Nil(t, res.Groups[0].Override, "expired override must be ignored")
}

func TestScanAllSkipsOrphanedOverrideWithoutGroup(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	db.Seed(seeded(overrideKey("ghost"), map[string]types.AttributeValue{
		"desired":    s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	}))

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

// 新規作成は既存の設定を上書きせず、既存更新は削除済みの設定を再作成してはならない。
func TestConditionalGroupWritesPreserveExistence(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	original := model.GroupSpec{Name: "dev", Mode: model.ModeDisabled, TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance}}
	require.NoError(t, st.CreateGroup(ctx, original))

	err := st.CreateGroup(ctx, model.GroupSpec{Name: "dev", Mode: model.ModePinned, Desired: model.DesiredRunning})
	assert.ErrorIs(t, err, ErrGroupAlreadyExists)
	got, err := st.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, original, *got)

	require.NoError(t, st.DeleteGroup(ctx, "dev"))
	err = st.UpdateGroup(ctx, "dev", GroupPatch{Mode: new(model.ModePinned), Desired: new(model.DesiredRunning)})
	assert.ErrorIs(t, err, ErrGroupNotFound)
	got, err = st.GetGroup(ctx, "dev")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// 属性単位の更新は、パッチに含まれないcronとセレクターを保持する。
// 異なる設定操作が同時に行われても、無関係な属性を古い読み取り結果で上書きしないためである。
func TestUpdateGroupLeavesUnspecifiedAttributesUntouched(t *testing.T) {
	db, st := newFixture(t)
	ctx := context.Background()
	require.NoError(t, st.PutGroup(ctx, model.GroupSpec{
		Name: "dev", Mode: model.ModeSchedule, StartCron: "0 9 * * *", StopCron: "0 20 * * *",
		TagKey: "env", TagValue: "dev", Types: []model.ResourceType{model.TypeRdsInstance},
	}))

	require.NoError(t, st.UpdateGroup(ctx, "dev", GroupPatch{Mode: new(model.ModeDisabled)}))

	got, err := st.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, model.ModeDisabled, got.Mode)
	assert.Equal(t, "0 9 * * *", got.StartCron)
	assert.Equal(t, "0 20 * * *", got.StopCron)
	assert.Equal(t, "env", got.TagKey)
	assert.Equal(t, "dev", got.TagValue)
	assert.Equal(t, []model.ResourceType{model.TypeRdsInstance}, got.Types)
	assert.NotNil(t, db.Item("CONFIG", "GROUP#dev"), "保存形式は複合キーでなければならない")
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

	db.Seed(seeded(overrideKey("expired"), map[string]types.AttributeValue{
		"desired":    s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "0"},
	}))
	got, err := st.GetOverride(ctx, "expired", now)
	require.NoError(t, err)
	assert.Nil(t, got, "an override past its expiry must read back as absent")

	db.Seed(seeded(overrideKey("broken"), map[string]types.AttributeValue{
		"desired":    s("not-a-valid-state"),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	}))
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
	db.FailOn("update", "status#rds-instance#a", assert.AnError) // 呼ばれたら気づけるようにしておく

	err := st.UpdateStatus(context.Background(), "rds-instance#a", StatusPatch{})

	require.NoError(t, err)
	assert.Nil(t, stored(db, statusKey("rds-instance#a")), "何も書くものがなければアイテムを作ってはならない")
}

// 空文字を指すポインタは属性の削除を表し、nil が表す変更なしとは異なる
// clearRecoveredError がエラーを削除する際に用いる経路であり、ポインタとする根拠に該当する
func TestUpdateStatusDistinguishesClearFromUntouched(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	require.NoError(t, st.UpdateStatus(ctx, "rds-instance#a", StatusPatch{
		LastError: new("boom"), LastAction: new(model.ActionStop),
	}))

	require.NoError(t, st.UpdateStatus(ctx, "rds-instance#a", StatusPatch{LastError: new("")}))

	got, err := st.GetStatus(ctx, "rds-instance#a")
	require.NoError(t, err)
	assert.Empty(t, got.LastError, "空文字を指すポインタは属性を消す")
	assert.Equal(t, model.ActionStop, got.LastAction, "パッチに含めなかった属性は触らない")
}

// Status の更新は expires_at を最後の更新時刻から保持期間後へ進める
// 更新されなくなった Status を DynamoDB TTL の自動削除対象とし、孤立 Status の蓄積を避ける
func TestStatusUpdateSetsAndRefreshesExpiration(t *testing.T) {
	db, st := newFixture(t, WithStatusRetention(7*24*time.Hour))
	current := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return current }

	require.NoError(t, st.UpdateStatus(context.Background(), "rds-instance#a", StatusPatch{LastError: new("first")}))
	item := stored(db, statusKey("rds-instance#a"))
	assert.Equal(t, fmt.Sprint(current.Add(7*24*time.Hour).Unix()), item["expires_at"].(*types.AttributeValueMemberN).Value)

	current = current.Add(2 * 24 * time.Hour)
	require.NoError(t, st.UpdateStatus(context.Background(), "rds-instance#a", StatusPatch{LastError: new("second")}))
	item = stored(db, statusKey("rds-instance#a"))
	assert.Equal(t, fmt.Sprint(current.Add(7*24*time.Hour).Unix()), item["expires_at"].(*types.AttributeValueMemberN).Value)
}

// 削除は pk の組み立てのみを行うが、種別ごとの接頭辞が正しくない場合、対象のアイテムが残り、対象外のアイテムが削除される
// 4 種類がそれぞれ対象のアイテムのみを削除することを確かめる
func TestDeletesTargetTheRightItem(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		seededKey itemKey
		remove    func(*Store) error
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
			db.Seed(seeded(overrideKey("dev"), map[string]types.AttributeValue{"desired": s(model.DesiredRunning),
				"expires_at": &types.AttributeValueMemberN{Value: "9999999999"}}))
			seedGroupStatus(db, "dev", map[string]types.AttributeValue{"last_error": s("boom")})
			seedStatus(db, "rds-instance#a", map[string]types.AttributeValue{"last_action": s("stop")})

			require.NoError(t, tc.remove(st))

			assert.Nil(t, stored(db, tc.seededKey), "%v must be deleted", tc.seededKey)
			for _, key := range []itemKey{groupKey("dev"), overrideKey("dev"), groupStatusKey("dev"), statusKey("rds-instance#a")} {
				if key != tc.seededKey {
					assert.NotNilf(t, stored(db, key), "%v must survive", key)
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
	assert.Equal(t, "CONFIG", GroupPK("dev"))
	assert.Equal(t, "GROUP#dev", GroupSK("dev"))
	assert.Equal(t, "CONFIG", OverridePK("dev"))
	assert.Equal(t, "OVERRIDE#dev", OverrideSK("dev"))
	assert.Equal(t, "STATUS#group#dev", GroupStatusPK("dev"))
	assert.Equal(t, "CURRENT", GroupStatusSK())
	assert.Equal(t, "STATUS#rds-instance#dev-db", StatusPK("rds-instance#dev-db"))
	assert.Equal(t, "CURRENT", StatusSK())

	// グループ単位のステータスは、合成リソース ID を用いた status# アイテムでなければならない
	assert.Equal(t, StatusPK(model.GroupStatusID("dev")), GroupStatusPK("dev"))
}

func TestGetPutStatusForGroupPseudoID(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	got, err := st.GetStatus(ctx, model.GroupStatusID("dev"))
	require.NoError(t, err)
	assert.Equal(t, model.Status{}, got)

	require.NoError(t, st.UpdateStatus(ctx, model.GroupStatusID("dev"), StatusPatch{LastError: new("discover: access denied")}))
	got, err = st.GetStatus(ctx, model.GroupStatusID("dev"))
	require.NoError(t, err)
	assert.Equal(t, "discover: access denied", got.LastError)
}

func TestReconcileLeaseExcludesConcurrentOwnerAndCanBeTakenAfterExpiry(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	acquired, err := st.AcquireLease(ctx, "owner-a", now, now.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, acquired)

	acquired, err = st.AcquireLease(ctx, "owner-b", now.Add(30*time.Second), now.Add(2*time.Minute))
	require.NoError(t, err)
	assert.False(t, acquired)

	acquired, err = st.AcquireLease(ctx, "owner-b", now.Add(61*time.Second), now.Add(3*time.Minute))
	require.NoError(t, err)
	assert.True(t, acquired)
	assert.Error(t, st.ReleaseLease(ctx, "owner-a"), "以前の所有者は新しいリースを解除できない")
	assert.NoError(t, st.ReleaseLease(ctx, "owner-b"))
}

func TestPendingOperationRequiresMatchingOperationID(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	resourceID := "rds-instance#dev"
	op := PendingOperation{
		ID: "op-a", Group: "dev", ConfigHash: strings.Repeat("a", 64),
		Action: model.ActionStop, Desired: model.DesiredStopped,
		Observed: model.StateRunning, StartedAt: "2026-08-30T12:00:00Z",
	}
	require.NoError(t, st.UpdateStatus(ctx, resourceID, StatusPatch{LastError: new("previous failure"), LastErrorAt: new("2026-08-30T11:59:00Z")}))

	require.NoError(t, st.BeginOperation(ctx, resourceID, op))
	pending, err := st.GetStatus(ctx, resourceID)
	require.NoError(t, err)
	assert.Equal(t, "dev", pending.PendingGroup)
	assert.Equal(t, strings.Repeat("a", 64), pending.PendingConfigHash)
	assert.Error(t, st.BeginOperation(ctx, resourceID, PendingOperation{ID: "op-b"}),
		"未完了の操作を別の操作で上書きしてはならない")
	assert.Error(t, st.CompleteOperation(ctx, resourceID, PendingOperation{ID: "op-b"}),
		"異なる操作IDでは完了へ進めてはならない")
	require.NoError(t, st.CompleteOperation(ctx, resourceID, op))

	got, err := st.GetStatus(ctx, resourceID)
	require.NoError(t, err)
	assert.Empty(t, got.PendingOperationID)
	assert.Empty(t, got.PendingGroup)
	assert.Empty(t, got.PendingConfigHash)
	assert.Equal(t, model.ActionStop, got.LastAction)
	assert.Equal(t, model.DesiredStopped, got.LastDesired)
	assert.Empty(t, got.LastError, "操作の完了と以前のエラー解除は同じ更新で確定する")

	require.NoError(t, st.BeginOperation(ctx, resourceID, PendingOperation{ID: "op-b"}),
		"完了記録後は通知の状態と無関係に次の操作を開始できなければならない")
}

// 旧バージョンが残した notification_pending は、新しい AWS 操作の開始条件に含めない
// デプロイ前に通知障害が発生していた場合も、通知の残存属性が AWS 操作を停止してはならない
func TestLegacyNotificationPendingDoesNotBlockOperation(t *testing.T) {
	db, st := newFixture(t)
	resourceID := "rds-instance#dev"
	seedStatus(db, resourceID, map[string]types.AttributeValue{"notification_pending": s("old-operation")})

	err := st.BeginOperation(context.Background(), resourceID, PendingOperation{
		ID: "new-operation", Action: model.ActionStop, Desired: model.DesiredStopped,
		Observed: model.StateRunning, StartedAt: "2026-08-30T12:00:00Z",
	})

	require.NoError(t, err)
}
