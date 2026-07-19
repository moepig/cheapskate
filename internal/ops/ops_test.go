package ops

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
	"cheapskate/internal/store"
)

func newFixture(t *testing.T) (*mocks.DynaStore, *store.Store) {
	t.Helper()
	ctrl := gomock.NewController(t)
	api, db := mocks.NewDynaStore(ctrl)
	return db, store.New(api, "t")
}

var now = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// A-4: pin -> schedule -> pin must round-trip the cron fields instead of losing them, since Pin/Schedule each read-modify-write the shared tag item.
func TestPinScheduleRoundTripPreservesCronFields(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	tag := "dev"

	_, err := Add(ctx, s, tag, "ecs#dev/api", 0)
	require.NoError(t, err)
	_, err = Schedule(ctx, s, tag, ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 18 * * *"})
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, tag, model.DesiredStopped))

	got, err := s.GetTag(ctx, tag)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "0 9 * * *", got.StartCron, "pin must preserve cron fields")
	assert.Equal(t, "0 18 * * *", got.StopCron, "pin must preserve cron fields")

	_, err = Schedule(ctx, s, tag, ScheduleSpec{StartCron: "0 8 * * *", StopCron: "0 20 * * *"})
	require.NoError(t, err)
	got, err = s.GetTag(ctx, tag)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "0 8 * * *", got.StartCron, "schedule must still update cron fields")
	assert.Equal(t, "0 20 * * *", got.StopCron, "schedule must still update cron fields")
}

func TestAddCreatesTagWhenAbsent(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()

	created, err := Add(ctx, s, "dev", "rds-instance#db", 0)
	require.NoError(t, err)
	assert.True(t, created, "first add must create the tag")

	tag, err := s.GetTag(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, tag)
	assert.Equal(t, model.ModeDisabled, tag.Mode)

	member, err := s.GetMember(ctx, "rds-instance#db")
	require.NoError(t, err)
	require.NotNil(t, member)
	assert.Equal(t, "dev", member.Tag)
	assert.Equal(t, model.TypeRdsInstance, member.Type)
}

func TestAddToExistingTagDoesNotRecreate(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	_, err := Add(ctx, s, "dev", "rds-instance#db", 0)
	require.NoError(t, err)

	created, err := Add(ctx, s, "dev", "ecs#dev-cluster/api", 0)
	require.NoError(t, err)
	assert.False(t, created, "adding to an already-existing tag must not report creation")
}

// B-9-equivalent for members: re-adding to the same tag without -restore-count must keep the existing restore_count, not silently drop it; an explicit value overwrites it.
func TestAddSameTagUpsertPreservesOrOverwritesRestoreCount(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	_, err := Add(ctx, s, "dev", "ecs#dev/api", 3)
	require.NoError(t, err)

	_, err = Add(ctx, s, "dev", "ecs#dev/api", 0)
	require.NoError(t, err)
	member, err := s.GetMember(ctx, "ecs#dev/api")
	require.NoError(t, err)
	require.NotNil(t, member.RestoreCount)
	assert.Equal(t, int32(3), *member.RestoreCount, "re-add without -restore-count must preserve it")

	_, err = Add(ctx, s, "dev", "ecs#dev/api", 5)
	require.NoError(t, err)
	member, err = s.GetMember(ctx, "ecs#dev/api")
	require.NoError(t, err)
	require.NotNil(t, member.RestoreCount)
	assert.Equal(t, int32(5), *member.RestoreCount, "explicit restore_count must overwrite")
}

func TestAddRejectsCrossTag(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	_, err := Add(ctx, s, "dev", "rds-instance#db", 0)
	require.NoError(t, err)

	_, err = Add(ctx, s, "prod", "rds-instance#db", 0)
	require.Error(t, err, "adding a resource already in a different tag must fail")
}

func TestAddRejectsRestoreCountOnNonEcs(t *testing.T) {
	_, s := newFixture(t)
	_, err := Add(context.Background(), s, "dev", "rds-instance#db", 3)
	require.Error(t, err, "restore count only applies to ecs resources")
}

func TestAssembleResourceID(t *testing.T) {
	id, err := AssembleResourceID("rds-instance", "db", "", "")
	require.NoError(t, err)
	assert.Equal(t, "rds-instance#db", id)

	id, err = AssembleResourceID("ecs", "", "dev-cluster", "api")
	require.NoError(t, err)
	assert.Equal(t, "ecs#dev-cluster/api", id)

	_, err = AssembleResourceID("", "", "", "")
	require.Error(t, err, "missing type must be rejected")
	_, err = AssembleResourceID("rds-instance", "", "", "")
	require.Error(t, err, "rds type without name must be rejected")
	_, err = AssembleResourceID("rds-instance", "db", "c", "")
	require.Error(t, err, "cluster/service with rds type must be rejected")
	_, err = AssembleResourceID("ecs", "", "c", "")
	require.Error(t, err, "ecs without service must be rejected")
	_, err = AssembleResourceID("ecs", "n", "c", "s")
	require.Error(t, err, "name with ecs type must be rejected")
	_, err = AssembleResourceID("sqs-queue", "", "", "")
	require.Error(t, err, "unknown type must be rejected")
}

// B-6: an override on an unregistered tag has no effect, so it is rejected.
func TestSetOverrideRejectsUnknownTag(t *testing.T) {
	_, s := newFixture(t)
	_, err := SetOverride(context.Background(), s, "ghost", model.DesiredRunning, time.Hour, now)
	require.Error(t, err, "want error for unknown tag")
}

// B-6: disabled is a stronger stop than override (the reconciler skips disabled tags before ever reading the override), so registering one would silently do nothing; SetOverride must reject it instead.
func TestSetOverrideRejectsDisabled(t *testing.T) {
	f, s := newFixture(t)
	ctx := context.Background()
	tag := "dev"
	_, err := Add(ctx, s, tag, "rds-instance#db", 0)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, tag, model.DesiredStopped))
	require.NoError(t, Disable(ctx, s, tag))

	_, err = SetOverride(ctx, s, tag, model.DesiredRunning, time.Hour, now)
	require.Error(t, err, "want error overriding a disabled tag")
	assert.Nil(t, f.Item("override#"+tag), "rejected override must not be written")
}

func TestSetOverrideAllowedWhenScheduledOrPinned(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	tag := "dev"
	_, err := Add(ctx, s, tag, "rds-instance#db", 0)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, tag, model.DesiredStopped))

	_, err = SetOverride(ctx, s, tag, model.DesiredRunning, time.Hour, now)
	require.NoError(t, err, "override on a pinned tag must be allowed")
}

func TestPinScheduleDisableRejectUnknownTag(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	require.Error(t, Pin(ctx, s, "ghost", model.DesiredStopped))
	_, err := Schedule(ctx, s, "ghost", ScheduleSpec{StartCron: "0 9 * * *"})
	require.Error(t, err)
	require.Error(t, Disable(ctx, s, "ghost"))
}

func TestRemoveMemberDeletesMemberAndStatus(t *testing.T) {
	f, s := newFixture(t)
	ctx := context.Background()
	tag := "dev"
	resourceID := "rds-instance#db"
	_, err := Add(ctx, s, tag, resourceID, 0)
	require.NoError(t, err)
	require.NoError(t, s.PutStatus(ctx, resourceID, map[string]any{"last_action": "stop"}))

	require.NoError(t, RemoveMember(ctx, s, tag, resourceID))
	assert.Nil(t, f.Item("member#"+resourceID))
	assert.Nil(t, f.Item("status#"+resourceID))

	tagItem, err := s.GetTag(ctx, tag)
	require.NoError(t, err)
	assert.NotNil(t, tagItem, "removing a member must not remove the tag itself")
}

func TestRemoveMemberRejectsWrongTag(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	_, err := Add(ctx, s, "dev", "rds-instance#db", 0)
	require.NoError(t, err)

	err = RemoveMember(ctx, s, "prod", "rds-instance#db")
	require.Error(t, err, "removing a member from the wrong tag must fail")
}

func TestRemoveTagDeletesTagMembersOverrideAndStatuses(t *testing.T) {
	f, s := newFixture(t)
	ctx := context.Background()
	tag := "dev"
	_, err := Add(ctx, s, tag, "rds-instance#db", 0)
	require.NoError(t, err)
	_, err = Add(ctx, s, tag, "ecs#dev-cluster/api", 0)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, tag, model.DesiredStopped))
	_, err = SetOverride(ctx, s, tag, model.DesiredRunning, time.Hour, now)
	require.NoError(t, err)
	require.NoError(t, s.PutStatus(ctx, "rds-instance#db", map[string]any{"last_action": "stop"}))
	require.NoError(t, s.PutStatus(ctx, "ecs#dev-cluster/api", map[string]any{"last_action": "stop"}))

	require.NoError(t, RemoveTag(ctx, s, tag))

	for _, pk := range []string{
		"tag#" + tag, "override#" + tag,
		"member#rds-instance#db", "status#rds-instance#db",
		"member#ecs#dev-cluster/api", "status#ecs#dev-cluster/api",
	} {
		assert.Nilf(t, f.Item(pk), "%s not deleted", pk)
	}
}

// B-5: List must keep going and report the bad row's error instead of aborting the whole list when one tag's override is malformed.
func TestListSurfacesPerRowErrorWithoutAbortingOthers(t *testing.T) {
	f, s := newFixture(t)
	ctx := context.Background()
	_, err := Add(ctx, s, "broken", "rds-instance#broken-db", 0)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, "broken", model.DesiredStopped))
	f.Seed(map[string]types.AttributeValue{
		"pk":         &types.AttributeValueMemberS{Value: "override#broken"},
		"desired":    &types.AttributeValueMemberS{Value: "not-a-valid-state"},
		"expires_at": &types.AttributeValueMemberN{Value: "9999999999"},
	})
	_, err = Add(ctx, s, "fine", "rds-instance#fine-db", 0)
	require.NoError(t, err)
	require.NoError(t, Pin(ctx, s, "fine", model.DesiredStopped))

	rows, err := List(ctx, s, now)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	byName := map[string]TagRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	assert.Error(t, byName["broken"].Err, "malformed row must carry its error")
	assert.NoError(t, byName["fine"].Err, "unrelated row must be unaffected")
}

// Get must resolve every member of the tag with its status, since the CLI's `show` and the
// console's tag page both need "the resources this tag currently applies to", not just the tag's
// own config item.
func TestGetResolvesMembersWithStatus(t *testing.T) {
	_, s := newFixture(t)
	ctx := context.Background()
	tag := "dev"
	_, err := Add(ctx, s, tag, "rds-instance#db", 0)
	require.NoError(t, err)
	_, err = Add(ctx, s, tag, "ecs#dev-cluster/api", 2)
	require.NoError(t, err)
	require.NoError(t, s.PutStatus(ctx, "rds-instance#db", map[string]any{"observed_state": model.StateStopped}))

	row, err := Get(ctx, s, tag, now)
	require.NoError(t, err)
	require.Len(t, row.Members, 2)
	byID := map[string]MemberRow{}
	for _, m := range row.Members {
		byID[m.ResourceID] = m
	}
	assert.Equal(t, model.StateStopped, byID["rds-instance#db"].Status.ObservedState)
	require.NotNil(t, byID["ecs#dev-cluster/api"].Member.RestoreCount)
	assert.Equal(t, int32(2), *byID["ecs#dev-cluster/api"].Member.RestoreCount)
}

func TestGetRejectsUnknownTag(t *testing.T) {
	_, s := newFixture(t)
	_, err := Get(context.Background(), s, "ghost", now)
	require.Error(t, err)
}
