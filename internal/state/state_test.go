package state

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"cheapskate/internal/core/model"
	"cheapskate/internal/state/mocks"
)

func newFixture(t *testing.T) (*mocks.DynaStore, *Store) {
	t.Helper()
	api, db := mocks.NewDynaStore(gomock.NewController(t))
	return db, New(api, "table")
}

func seed(db *mocks.DynaStore, group model.GroupSpec, extra map[string]types.AttributeValue) {
	item := encodeGroup(group)
	maps.Copy(item, extra)
	db.Seed(item)
}

func TestListGroupsQueriesEveryPageWithStrongConsistency(t *testing.T) {
	db, store := newFixture(t)
	db.SetQueryPageSize(1)
	seed(db, model.GroupSpec{Name: "b", Override: model.OverrideRunning}, nil)
	seed(db, model.GroupSpec{Name: "a", Override: model.OverrideStopped}, nil)

	rows, err := store.ListGroups(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "a", rows[0].Name)
	assert.Equal(t, "b", rows[1].Name)
	assert.Equal(t, 2, db.Calls("query"))
	assert.True(t, db.QueryConsistentRead())
}

func TestListGroupsFailsWhenTheSecondPageQueryFails(t *testing.T) {
	controller := gomock.NewController(t)
	api := mocks.NewMockAPI(controller)
	first := encodeGroup(model.GroupSpec{Name: "a", Override: model.OverrideRunning})
	lastKey := map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: configPK},
		"sk": &types.AttributeValueMemberS{Value: groupSKPrefix + "a"},
	}
	queryErr := errors.New("second page unavailable")
	gomock.InOrder(
		api.EXPECT().Query(gomock.Any(), gomock.Any()).DoAndReturn(
			func(_ context.Context, input *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
				assert.True(t, aws.ToBool(input.ConsistentRead))
				return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{first}, LastEvaluatedKey: lastKey}, nil
			}),
		api.EXPECT().Query(gomock.Any(), gomock.Any()).Return(nil, queryErr),
	)

	rows, err := New(api, "table").ListGroups(context.Background())

	assert.Nil(t, rows)
	assert.ErrorIs(t, err, queryErr)
	assert.ErrorContains(t, err, "query group configuration")
}

func TestGetGroupUsesStrongConsistency(t *testing.T) {
	db, store := newFixture(t)
	seed(db, model.GroupSpec{Name: "dev", Override: model.OverrideRunning}, nil)

	group, err := store.GetGroup(context.Background(), "dev")

	require.NoError(t, err)
	require.NotNil(t, group)
	assert.True(t, db.GetConsistentRead())
}

func TestStrictDecodeRejectsUnknownAndMalformedAttributes(t *testing.T) {
	db, store := newFixture(t)
	seed(db, model.GroupSpec{Name: "typo", Override: model.OverrideRunning}, map[string]types.AttributeValue{
		"override_expire_at": &types.AttributeValueMemberN{Value: "999"},
	})
	seed(db, model.GroupSpec{Name: "decimal", Override: model.OverrideRunning}, map[string]types.AttributeValue{
		"override_expires_at": &types.AttributeValueMemberN{Value: "1.5"},
	})
	seed(db, model.GroupSpec{Name: "exponent", Override: model.OverrideRunning}, map[string]types.AttributeValue{
		"override_expires_at": &types.AttributeValueMemberN{Value: "1e3"},
	})

	rows, err := store.ListGroups(context.Background())
	require.NoError(t, err)
	byName := map[string]error{}
	for _, row := range rows {
		byName[row.Name] = row.Err
	}
	assert.ErrorContains(t, byName["typo"], "unknown attributes")
	assert.ErrorContains(t, byName["decimal"], "positive decimal integer")
	assert.ErrorContains(t, byName["exponent"], "positive decimal integer")
}

func TestScheduleAndOverrideUpdatesPreserveEachOther(t *testing.T) {
	db, store := newFixture(t)
	ctx := context.Background()
	group := model.GroupSpec{Name: "dev", StartCron: "0 9 * * *", StopCron: "0 20 * * *", Override: model.OverrideRunning, EcsMaxCount: 3}
	require.NoError(t, store.CreateGroup(ctx, group))
	require.NoError(t, store.SetSchedule(ctx, "dev", model.ScheduleSpec{StartCron: "0 8 * * *", StopCron: "0 19 * * *"}))
	require.NoError(t, store.SetOverride(ctx, "dev", model.OverrideStopped, 12345))

	got, err := store.GetGroup(ctx, "dev")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "0 8 * * *", got.StartCron)
	assert.Equal(t, "0 19 * * *", got.StopCron)
	assert.Equal(t, model.OverrideStopped, got.Override)
	assert.EqualValues(t, 12345, got.OverrideExpiresAt)
	assert.EqualValues(t, 3, got.EcsMaxCount)

	require.NoError(t, store.SetOverride(ctx, "dev", model.OverrideDisabled, 0))
	got, err = store.GetGroup(ctx, "dev")
	require.NoError(t, err)
	assert.Zero(t, got.OverrideExpiresAt)
	assert.Equal(t, "0 8 * * *", got.StartCron)
	require.NoError(t, store.ClearOverride(ctx, "dev"))
	got, err = store.GetGroup(ctx, "dev")
	require.NoError(t, err)
	assert.EqualValues(t, 3, got.EcsMaxCount)
	assert.NotNil(t, db.Item(configPK, groupSKPrefix+"dev"))
}

// Number 型の正の int32 だけを上限として復号し、不正な登録値を設定エラーとして返す。
func TestGroupCountLimitDecoding(t *testing.T) {
	for name, test := range map[string]struct {
		value   types.AttributeValue
		want    int32
		wantErr string
	}{
		"absent":        {},
		"positive":      {value: &types.AttributeValueMemberN{Value: "3"}, want: 3},
		"int32 maximum": {value: &types.AttributeValueMemberN{Value: "2147483647"}, want: 2147483647},
		"string":        {value: &types.AttributeValueMemberS{Value: "3"}, wantErr: "must be a Number"},
		"null":          {value: &types.AttributeValueMemberNULL{Value: true}, wantErr: "must be a Number"},
		"zero":          {value: &types.AttributeValueMemberN{Value: "0"}, wantErr: "positive decimal integer"},
		"negative":      {value: &types.AttributeValueMemberN{Value: "-1"}, wantErr: "positive decimal integer"},
		"decimal":       {value: &types.AttributeValueMemberN{Value: "1.5"}, wantErr: "positive decimal integer"},
		"exponent":      {value: &types.AttributeValueMemberN{Value: "1e3"}, wantErr: "positive decimal integer"},
		"empty":         {value: &types.AttributeValueMemberN{Value: ""}, wantErr: "positive decimal integer"},
		"overflow":      {value: &types.AttributeValueMemberN{Value: "2147483648"}, wantErr: "out of range"},
	} {
		t.Run(name, func(t *testing.T) {
			db, store := newFixture(t)
			extra := map[string]types.AttributeValue{}
			if test.value != nil {
				extra["ecs_max_count"] = test.value
			}
			seed(db, model.GroupSpec{Name: "dev", Override: model.OverrideRunning}, extra)
			group, err := store.GetGroup(context.Background(), "dev")
			if test.wantErr != "" {
				assert.ErrorIs(t, err, ErrInvalidGroup)
				assert.ErrorContains(t, err, test.wantErr)
				rows, err := store.ListGroups(context.Background())
				require.NoError(t, err)
				require.Len(t, rows, 1)
				assert.Equal(t, "dev", rows[0].Name)
				assert.ErrorContains(t, rows[0].Err, test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, group.EcsMaxCount)
		})
	}
}

func TestStaleUpdatesDoNotRecreateDeletedGroup(t *testing.T) {
	db, store := newFixture(t)
	ctx := context.Background()
	require.NoError(t, store.CreateGroup(ctx, model.GroupSpec{Name: "dev", Override: model.OverrideRunning}))
	require.NoError(t, store.DeleteGroup(ctx, "dev"))

	err := store.SetSchedule(ctx, "dev", model.ScheduleSpec{StartCron: "0 9 * * *", StopCron: "0 20 * * *"})
	assert.ErrorIs(t, err, ErrConflict)
	err = store.ClearOverride(ctx, "dev")
	assert.ErrorIs(t, err, ErrConflict)
	err = store.SetOverride(ctx, "dev", model.OverrideStopped, 0)
	assert.ErrorIs(t, err, ErrConflict)
	assert.Nil(t, db.Item(configPK, groupSKPrefix+"dev"))
}

func TestTimedOverrideAndClearRequireSchedule(t *testing.T) {
	_, store := newFixture(t)
	ctx := context.Background()
	require.NoError(t, store.CreateGroup(ctx, model.GroupSpec{Name: "dev", Override: model.OverrideRunning}))
	assert.ErrorIs(t, store.SetOverride(ctx, "dev", model.OverrideStopped, 12345), ErrConflict)
	assert.ErrorIs(t, store.ClearOverride(ctx, "dev"), ErrConflict)
}

func TestCreateAndDeleteAreConditional(t *testing.T) {
	_, store := newFixture(t)
	ctx := context.Background()
	group := model.GroupSpec{Name: "dev", Override: model.OverrideRunning}
	require.NoError(t, store.CreateGroup(ctx, group))
	assert.ErrorIs(t, store.CreateGroup(ctx, group), ErrConflict)
	require.NoError(t, store.DeleteGroup(ctx, "dev"))
	assert.ErrorIs(t, store.DeleteGroup(ctx, "dev"), ErrConflict)
}
