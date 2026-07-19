package store

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/mocks"
	"cheapskate/internal/model"
)

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func newFixture(t *testing.T) (*mocks.DynaStore, *Store) {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	return db, New(api, "t")
}

func seedTag(db *mocks.DynaStore, name, mode, desired string) {
	db.Seed(map[string]types.AttributeValue{
		"pk": s(model.TagPrefix + name), "mode": s(mode), "desired": s(desired),
	})
}

func seedMember(db *mocks.DynaStore, tag, resourceID, typ string) {
	db.Seed(map[string]types.AttributeValue{
		"pk": s(model.MemberPrefix + resourceID), "tag": s(tag), "type": s(typ),
	})
}

// A-6/C-1: ScanAll must page through Scan via LastEvaluatedKey rather than assuming one page, since a real table's Scan is capped at 1MB per call.
func TestScanAllPagesThroughScan(t *testing.T) {
	db, st := newFixture(t)
	db.SetScanPageSize(1)
	seedTag(db, "a", model.ModePinned, model.DesiredStopped)
	seedTag(db, "b", model.ModePinned, model.DesiredStopped)
	seedTag(db, "c", model.ModePinned, model.DesiredStopped)

	rows, err := st.ScanAll(context.Background(), time.Now())
	require.NoError(t, err)
	assert.Len(t, rows, 3)
}

func TestScanAllJoinsTagMemberOverrideStatus(t *testing.T) {
	db, st := newFixture(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	seedTag(db, "dev", model.ModePinned, model.DesiredStopped)
	seedMember(db, "dev", "rds-instance#a", model.TypeRdsInstance)
	db.Seed(map[string]types.AttributeValue{
		"pk": s(model.OverridePrefix + "dev"), "desired": s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	db.Seed(map[string]types.AttributeValue{
		"pk": s(model.StatusPrefix + "rds-instance#a"), "last_action": s("stop"),
	})

	rows, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	r := rows[0]
	assert.True(t, r.HasTag)
	require.NotNil(t, r.Override)
	assert.Equal(t, model.DesiredRunning, r.Override.Desired)
	require.Len(t, r.Members, 1)
	assert.Equal(t, "rds-instance#a", r.Members[0].ResourceID)
	assert.Equal(t, "stop", r.Members[0].Status.LastAction)
	assert.NoError(t, r.Err)
}

func TestScanAllGroupsMultipleMembersUnderOneTag(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	seedTag(db, "dev", model.ModeSchedule, "")
	seedMember(db, "dev", "rds-instance#a", model.TypeRdsInstance)
	seedMember(db, "dev", "ecs#dev-cluster/api", model.TypeEcsService)

	rows, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Len(t, rows[0].Members, 2)
	assert.Equal(t, "ecs#dev-cluster/api", rows[0].Members[0].ResourceID, "members must be sorted by resource_id")
	assert.Equal(t, "rds-instance#a", rows[0].Members[1].ResourceID)
}

// B-5: a malformed override item for one tag must not prevent other tags from being listed, and must surface as that row's Err instead of aborting the scan.
func TestScanAllRecordsPerRowErrorForMalformedOverride(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	seedTag(db, "broken", model.ModePinned, model.DesiredStopped)
	db.Seed(map[string]types.AttributeValue{
		"pk": s(model.OverridePrefix + "broken"), "desired": s("not-a-valid-state"),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	seedTag(db, "fine", model.ModePinned, model.DesiredStopped)

	rows, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byName := map[string]TagRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	assert.Error(t, byName["broken"].Err, "malformed override must set Err on its row")
	assert.True(t, byName["broken"].HasTag, "tag must still be joined despite the bad override")
	assert.NoError(t, byName["fine"].Err, "unrelated row must be unaffected")
}

// B-5 for members: a member item that fails to unmarshal (attribute type mismatch — the "tag"
// field is corrupted, so which TagRow it belongs to is unknowable) must not abort the scan for
// other tags; it lands on the untagged/"" row instead of being silently dropped.
func TestScanAllRecordsPerRowErrorForUnmarshalableMember(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	db.Seed(map[string]types.AttributeValue{
		"pk":   s(model.MemberPrefix + "rds-instance#a"),
		"tag":  &types.AttributeValueMemberBOOL{Value: true}, // "tag" must be a string; this fails UnmarshalMap
		"type": s(model.TypeRdsInstance),
	})
	seedTag(db, "fine", model.ModePinned, model.DesiredStopped)

	rows, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	byName := map[string]TagRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	assert.Error(t, byName[""].Err, "unmarshalable member must not be silently dropped")
	assert.False(t, byName[""].HasTag, "the untagged row must not be treated as a real tag")
	assert.NoError(t, byName["fine"].Err, "unrelated row must be unaffected")
}

func TestScanAllExpiredOverrideIsIgnored(t *testing.T) {
	db, st := newFixture(t)
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	seedTag(db, "dev", model.ModePinned, model.DesiredStopped)
	db.Seed(map[string]types.AttributeValue{
		"pk": s(model.OverridePrefix + "dev"), "desired": s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "1"}, // long past
	})

	rows, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Nil(t, rows[0].Override, "expired override must be ignored")
}

func TestScanAllSkipsOrphanedOverrideWithoutTag(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	db.Seed(map[string]types.AttributeValue{
		"pk": s(model.OverridePrefix + "ghost"), "desired": s(model.DesiredRunning),
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})

	rows, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].HasTag, "orphaned override must be reported without HasTag")
}

func TestScanAllReportsOrphanedMembersWithoutTag(t *testing.T) {
	db, st := newFixture(t)
	now := time.Now()
	seedMember(db, "ghost", "rds-instance#a", model.TypeRdsInstance)

	rows, err := st.ScanAll(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.False(t, rows[0].HasTag)
	require.Len(t, rows[0].Members, 1)
}

func TestGetPutTag(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	got, err := st.GetTag(ctx, "dev")
	require.NoError(t, err)
	assert.Nil(t, got)

	require.NoError(t, st.PutTag(ctx, model.TagItem{PK: model.TagPrefix + "dev", Mode: model.ModeDisabled}))
	got, err = st.GetTag(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, model.ModeDisabled, got.Mode)
}

func TestGetPutMember(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	got, err := st.GetMember(ctx, "rds-instance#db")
	require.NoError(t, err)
	assert.Nil(t, got)

	require.NoError(t, st.PutMember(ctx, model.MemberItem{PK: model.MemberPrefix + "rds-instance#db", Tag: "dev", Type: model.TypeRdsInstance}))
	got, err = st.GetMember(ctx, "rds-instance#db")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "dev", got.Tag)
}

func TestCreateMemberEnforcesOneTagInvariant(t *testing.T) {
	_, st := newFixture(t)
	ctx := context.Background()
	item := model.MemberItem{PK: model.MemberPrefix + "rds-instance#db", Tag: "dev", Type: model.TypeRdsInstance}
	require.NoError(t, st.CreateMember(ctx, item))

	err := st.CreateMember(ctx, model.MemberItem{PK: model.MemberPrefix + "rds-instance#db", Tag: "prod", Type: model.TypeRdsInstance})
	require.ErrorIs(t, err, ErrMemberExists)

	// The original membership must be untouched by the rejected attempt.
	got, err := st.GetMember(ctx, "rds-instance#db")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "dev", got.Tag)
}

func TestListMembersFiltersByTag(t *testing.T) {
	db, st := newFixture(t)
	ctx := context.Background()
	seedMember(db, "dev", "rds-instance#a", model.TypeRdsInstance)
	seedMember(db, "dev", "ecs#dev-cluster/api", model.TypeEcsService)
	seedMember(db, "prod", "rds-instance#b", model.TypeRdsInstance)

	members, err := st.ListMembers(ctx, "dev")
	require.NoError(t, err)
	require.Len(t, members, 2)
	assert.Equal(t, "member#ecs#dev-cluster/api", members[0].PK)
	assert.Equal(t, "member#rds-instance#a", members[1].PK)
}

func TestListMembersPagesThroughScan(t *testing.T) {
	db, st := newFixture(t)
	db.SetScanPageSize(1)
	seedMember(db, "dev", "rds-instance#a", model.TypeRdsInstance)
	seedMember(db, "dev", "rds-instance#b", model.TypeRdsInstance)
	seedMember(db, "prod", "rds-instance#c", model.TypeRdsInstance)

	members, err := st.ListMembers(context.Background(), "dev")
	require.NoError(t, err)
	assert.Len(t, members, 2)
}

func TestGetOverrideByTagName(t *testing.T) {
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
