//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/emutest"
	"cheapskate/internal/model"
	"cheapskate/internal/store"
)

func newStore(t *testing.T) *store.Store {
	cfg := emutest.Config(t)
	table := emutest.CreateStateTable(t, cfg)
	return store.New(dynamodb.NewFromConfig(cfg), table)
}

func TestListMembersFiltersByTagAndSorts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	for _, item := range []model.MemberItem{
		{PK: "member#rds-instance#b-db", Tag: "dev", Type: model.TypeRdsInstance},
		{PK: "member#rds-instance#a-db", Tag: "dev", Type: model.TypeRdsInstance},
		{PK: "member#rds-instance#other", Tag: "prod", Type: model.TypeRdsInstance},
	} {
		require.NoError(t, s.PutMember(ctx, item))
	}
	// Non-member items must be excluded from the listing.
	require.NoError(t, s.PutStatus(ctx, "rds-instance#a-db", map[string]any{"last_action": "stop"}))
	require.NoError(t, s.PutOverride(ctx, "dev", model.Override{Desired: model.DesiredRunning, ExpiresAt: time.Now().Add(time.Hour).Unix()}))

	items, err := s.ListMembers(ctx, "dev")
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "member#rds-instance#a-db", items[0].PK)
	assert.Equal(t, "member#rds-instance#b-db", items[1].PK)
}

// CreateMember's atomicity is the one behavior the in-memory fake only approximates; verify the
// real conditional-put semantics against the emulator.
func TestCreateMemberConditionEnforcedByRealDynamoDB(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	require.NoError(t, s.CreateMember(ctx, model.MemberItem{PK: "member#rds-instance#db", Tag: "dev", Type: model.TypeRdsInstance}))

	err := s.CreateMember(ctx, model.MemberItem{PK: "member#rds-instance#db", Tag: "prod", Type: model.TypeRdsInstance})
	require.ErrorIs(t, err, store.ErrMemberExists)

	got, err := s.GetMember(ctx, "rds-instance#db")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "dev", got.Tag, "the rejected conflicting write must not have taken effect")
}

func TestOverrideExpiryEnforcedInCode(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, s.PutOverride(ctx, "dev", model.Override{Desired: model.DesiredRunning, ExpiresAt: now.Add(time.Hour).Unix()}))
	o, err := s.GetOverride(ctx, "dev", now)
	require.NoError(t, err)
	require.NotNil(t, o)
	assert.Equal(t, model.DesiredRunning, o.Desired)

	// TTL deletion is lazy; the store must treat a past expires_at as absent.
	o, err = s.GetOverride(ctx, "dev", now.Add(2*time.Hour))
	require.NoError(t, err)
	assert.Nil(t, o, "expired override must be nil")
}

func TestStatusRoundtrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	err := s.PutStatus(ctx, "ecs#dev/api", map[string]any{
		"last_action":         "stop",
		"saved_desired_count": int32(3),
		"saved_scaling_min":   int32(2),
		"ignored":             nil, // nil attrs must be skipped, not written
	})
	require.NoError(t, err)
	// A second partial update must merge, not replace.
	require.NoError(t, s.PutStatus(ctx, "ecs#dev/api", map[string]any{"last_action": "start"}))

	status, err := s.GetStatus(ctx, "ecs#dev/api")
	require.NoError(t, err)
	assert.Equal(t, "start", status.LastAction)
	require.NotNil(t, status.SavedDesiredCount)
	assert.Equal(t, int32(3), *status.SavedDesiredCount)
	require.NotNil(t, status.SavedScalingMin)
	assert.Equal(t, int32(2), *status.SavedScalingMin)
}
