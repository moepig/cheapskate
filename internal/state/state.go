package state

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"cheapskate/internal/core/model"
)

//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks cheapskate/internal/state API

type API interface {
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

type Store struct {
	db    API
	table string
}

func New(db API, table string) *Store {
	return &Store{db: db, table: table}
}

type GroupRow struct {
	Name  string
	Group model.GroupSpec
	Err   error
}

func (s *Store) ListGroups(ctx context.Context) ([]GroupRow, error) {
	var rows []GroupRow
	var startKey map[string]types.AttributeValue
	for {
		out, err := s.db.Query(ctx, &dynamodb.QueryInput{
			TableName:                 &s.table,
			ConsistentRead:            aws.Bool(true),
			KeyConditionExpression:    aws.String("#pk = :pk"),
			ExpressionAttributeNames:  map[string]string{"#pk": "pk"},
			ExpressionAttributeValues: map[string]types.AttributeValue{":pk": &types.AttributeValueMemberS{Value: configPK}},
			ExclusiveStartKey:         startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("query group configuration: %w", err)
		}
		for _, raw := range out.Items {
			group, decodeErr := decodeGroup(raw)
			name := group.Name
			if name == "" {
				if sk, ok := raw["sk"].(*types.AttributeValueMemberS); ok {
					if parsed, found := strings.CutPrefix(sk.Value, groupSKPrefix); found {
						name = parsed
					} else {
						name = sk.Value
					}
				}
			}
			rows = append(rows, GroupRow{Name: name, Group: group, Err: decodeErr})
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows, nil
}

func (s *Store) GetGroup(ctx context.Context, name string) (*model.GroupSpec, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: &s.table, Key: marshalKey(groupKey(name)), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("get group %s: %w", name, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	group, err := decodeGroup(out.Item)
	if err != nil {
		return nil, fmt.Errorf("%w: group %s: %v", ErrInvalidGroup, name, err)
	}
	return &group, nil
}

var (
	ErrGroupNotFound = errors.New("group not found")
	ErrInvalidGroup  = errors.New("invalid group configuration")
	ErrConflict      = errors.New("configuration conflict")
)

func (s *Store) CreateGroup(ctx context.Context, group model.GroupSpec) error {
	_, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                &s.table,
		Item:                     encodeGroup(group),
		ConditionExpression:      aws.String("attribute_not_exists(#pk)"),
		ExpressionAttributeNames: map[string]string{"#pk": "pk"},
	})
	return writeResult("create group "+group.Name, err)
}

func (s *Store) SetSchedule(ctx context.Context, name string, schedule model.ScheduleSpec) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           &s.table,
		Key:                 marshalKey(groupKey(name)),
		UpdateExpression:    aws.String("SET #start = :start, #stop = :stop"),
		ConditionExpression: aws.String("attribute_exists(#pk)"),
		ExpressionAttributeNames: map[string]string{
			"#pk": "pk", "#start": "start_cron", "#stop": "stop_cron",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":start": &types.AttributeValueMemberS{Value: schedule.StartCron},
			":stop":  &types.AttributeValueMemberS{Value: schedule.StopCron},
		},
	})
	return writeResult("set schedule for group "+name, err)
}

func (s *Store) SetOverride(ctx context.Context, name string, override model.Override, expiresAt int64) error {
	names := map[string]string{"#pk": "pk", "#override": "override", "#expires": "override_expires_at"}
	values := map[string]types.AttributeValue{":override": &types.AttributeValueMemberS{Value: string(override)}}
	update := "SET #override = :override REMOVE #expires"
	if expiresAt != 0 {
		values[":expires"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(expiresAt, 10)}
		update = "SET #override = :override, #expires = :expires"
	}
	condition := "attribute_exists(#pk)"
	if expiresAt != 0 {
		names["#start"], names["#stop"] = "start_cron", "stop_cron"
		condition += " AND attribute_exists(#start) AND attribute_exists(#stop)"
	}
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table, Key: marshalKey(groupKey(name)), UpdateExpression: &update,
		ConditionExpression: &condition, ExpressionAttributeNames: names, ExpressionAttributeValues: values,
	})
	return writeResult("set override for group "+name, err)
}

func (s *Store) ClearOverride(ctx context.Context, name string) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           &s.table,
		Key:                 marshalKey(groupKey(name)),
		UpdateExpression:    aws.String("REMOVE #override, #expires"),
		ConditionExpression: aws.String("attribute_exists(#pk) AND attribute_exists(#start) AND attribute_exists(#stop)"),
		ExpressionAttributeNames: map[string]string{
			"#pk": "pk", "#start": "start_cron", "#stop": "stop_cron", "#override": "override", "#expires": "override_expires_at",
		},
	})
	return writeResult("clear override for group "+name, err)
}

func (s *Store) DeleteGroup(ctx context.Context, name string) error {
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                &s.table,
		Key:                      marshalKey(groupKey(name)),
		ConditionExpression:      aws.String("attribute_exists(#pk)"),
		ExpressionAttributeNames: map[string]string{"#pk": "pk"},
	})
	return writeResult("delete group "+name, err)
}

func writeResult(operation string, err error) error {
	if err == nil {
		return nil
	}
	var conditional *types.ConditionalCheckFailedException
	if errors.As(err, &conditional) {
		return fmt.Errorf("%w: %s", ErrConflict, operation)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func GroupPK(string) string      { return configPK }
func GroupSK(name string) string { return groupKey(name).SK }
