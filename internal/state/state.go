package state

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"cheapskate/internal/backoff"
	"cheapskate/internal/core/model"
)

//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks cheapskate/internal/state API

type API interface {
	Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	BatchGetItem(ctx context.Context, in *dynamodb.BatchGetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.BatchGetItemOutput, error)
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
}

type Store struct {
	db              API
	table           string
	batchGetBackoff backoff.Exponential
}

func New(db API, table string) *Store {
	return &Store{
		db:              db,
		table:           table,
		batchGetBackoff: backoff.NewExponential(25*time.Millisecond, time.Second),
	}
}

type GroupRow struct {
	Name     string
	Group    model.GroupSpec
	HasGroup bool
	Override *model.Override
	Status   model.Status
	Err      error
}

type ScanResult struct {
	Groups   []GroupRow
	Statuses map[string]model.Status
}

// StatusRecordはstatusの値と、そのアイテムだけに限定された復号エラーを保持する。
type StatusRecord struct {
	Status model.Status
	Err    error
}

// ListGroupsは設定partitionを一貫性の強い読み取りで取得し、グループ単位のstatusを結合する。
func (s *Store) ListGroups(ctx context.Context, now time.Time) ([]GroupRow, error) {
	var raws []map[string]types.AttributeValue
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
		raws = append(raws, out.Items...)
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}

	rows := decodeConfigRows(raws, now)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, model.GroupStatusID(row.Name))
	}
	statuses, err := s.GetStatuses(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if record, ok := statuses[model.GroupStatusID(rows[i].Name)]; ok {
			rows[i].Status = record.Status
			if record.Err != nil {
				rows[i].Err = record.Err
			}
		}
	}
	return rows, nil
}

// GetGroupRowはグループ設定、override、グループ単位のstatusを1回のBatchGetItemで取得する。
func (s *Store) GetGroupRow(ctx context.Context, name string, now time.Time) (GroupRow, error) {
	raws, err := s.batchGet(ctx, []itemKey{groupKey(name), overrideKey(name), groupStatusKey(name)})
	if err != nil {
		return GroupRow{}, err
	}
	rows := decodeConfigRows(raws, now)
	row := GroupRow{Name: name}
	for _, candidate := range rows {
		if candidate.Name == name {
			row = candidate
			break
		}
	}
	for _, raw := range raws {
		pk, sk, ok := rawKey(raw)
		if !ok || pk != groupStatusKey(name).PK || sk != currentSK {
			continue
		}
		var item statusItem
		if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
			row.Err = fmt.Errorf("unmarshal group status %s: %w", name, err)
		} else {
			row.Status = item.status()
		}
	}
	return row, nil
}

// ScanAllはdoctor向けにテーブル全体を走査する。
func (s *Store) ScanAll(ctx context.Context, now time.Time) (ScanResult, error) {
	var raws []map[string]types.AttributeValue
	var startKey map[string]types.AttributeValue
	for {
		out, err := s.db.Scan(ctx, &dynamodb.ScanInput{TableName: &s.table, ExclusiveStartKey: startKey})
		if err != nil {
			return ScanResult{}, fmt.Errorf("scan all: %w", err)
		}
		raws = append(raws, out.Items...)
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		startKey = out.LastEvaluatedKey
	}

	rows := decodeConfigRows(raws, now)
	statuses := map[string]model.Status{}
	for _, raw := range raws {
		pk, sk, ok := rawKey(raw)
		if !ok || !strings.HasPrefix(pk, statusPKPrefix) || sk != currentSK {
			continue
		}
		resourceID := strings.TrimPrefix(pk, statusPKPrefix)
		var item statusItem
		if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
			if name, ok := model.GroupFromStatusID(resourceID); ok {
				row := rowForName(&rows, name)
				row.Err = fmt.Errorf("unmarshal group status %s: %w", name, err)
			}
			continue
		}
		if name, ok := model.GroupFromStatusID(resourceID); ok {
			rowForName(&rows, name).Status = item.status()
		} else {
			statuses[resourceID] = item.status()
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return ScanResult{Groups: rows, Statuses: statuses}, nil
}

func decodeConfigRows(raws []map[string]types.AttributeValue, now time.Time) []GroupRow {
	rows := map[string]*GroupRow{}
	var order []string
	rowFor := func(name string) *GroupRow {
		if row, ok := rows[name]; ok {
			return row
		}
		row := &GroupRow{Name: name}
		rows[name] = row
		order = append(order, name)
		return row
	}
	for _, raw := range raws {
		pk, sk, ok := rawKey(raw)
		if !ok || pk != configPK {
			continue
		}
		switch {
		case strings.HasPrefix(sk, groupSKPrefix):
			name := strings.TrimPrefix(sk, groupSKPrefix)
			var item groupItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				rowFor(name).Err = fmt.Errorf("unmarshal group %s: %w", name, err)
				continue
			}
			row := rowFor(name)
			row.Group, row.HasGroup = item.spec(name), true
		case strings.HasPrefix(sk, overrideSKPrefix):
			name := strings.TrimPrefix(sk, overrideSKPrefix)
			var item overrideItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				rowFor(name).Err = fmt.Errorf("unmarshal override %s: %w", name, err)
				continue
			}
			override := item.override()
			if override.ExpiresAt <= now.Unix() {
				continue
			}
			if override.Desired.Validate() != nil {
				rowFor(name).Err = fmt.Errorf("%s: override desired must be running|stopped", name)
				continue
			}
			rowFor(name).Override = &override
		}
	}
	sort.Strings(order)
	out := make([]GroupRow, 0, len(order))
	for _, name := range order {
		out = append(out, *rows[name])
	}
	return out
}

func rowForName(rows *[]GroupRow, name string) *GroupRow {
	for i := range *rows {
		if (*rows)[i].Name == name {
			return &(*rows)[i]
		}
	}
	*rows = append(*rows, GroupRow{Name: name})
	return &(*rows)[len(*rows)-1]
}

func (s *Store) GetGroup(ctx context.Context, name string) (*model.GroupSpec, error) {
	raw, err := s.get(ctx, groupKey(name))
	if err != nil || raw == nil {
		return nil, err
	}
	var item groupItem
	if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
		return nil, fmt.Errorf("unmarshal group %s: %w", name, err)
	}
	spec := item.spec(name)
	return &spec, nil
}

var (
	ErrGroupAlreadyExists = errors.New("group already exists")
	ErrGroupNotFound      = errors.New("group not found")
)

// グループ設定の全属性を無条件で置き換える。
// 既存データの投入とテスト用であり、設定操作は CreateGroup または UpdateGroup を用いる。
func (s *Store) PutGroup(ctx context.Context, spec model.GroupSpec) error {
	return s.putGroup(ctx, spec, false)
}

// グループが存在しない場合だけ、設定の全属性を作成する。
func (s *Store) CreateGroup(ctx context.Context, spec model.GroupSpec) error {
	return s.putGroup(ctx, spec, true)
}

func (s *Store) putGroup(ctx context.Context, spec model.GroupSpec, createOnly bool) error {
	item, err := attributevalue.MarshalMap(newGroupItem(spec))
	if err != nil {
		return fmt.Errorf("marshal group %s: %w", spec.Name, err)
	}
	in := &dynamodb.PutItemInput{TableName: &s.table, Item: item}
	if createOnly {
		in.ConditionExpression = aws.String("attribute_not_exists(#pk)")
		in.ExpressionAttributeNames = map[string]string{"#pk": "pk"}
	}
	_, err = s.db.PutItem(ctx, in)
	if createOnly && isConditionalCheckFailed(err) {
		return fmt.Errorf("%w: %s", ErrGroupAlreadyExists, spec.Name)
	}
	return err
}

// 既存グループの指定された設定属性だけを原子的に変更する。
// 対象が存在しない場合は ErrGroupNotFound を返す。
func (s *Store) UpdateGroup(ctx context.Context, name string, patch GroupPatch) error {
	type attr struct {
		name  string
		value types.AttributeValue
		empty bool
	}
	var attrs []attr
	addString := func(name string, value *string) {
		if value != nil {
			attrs = append(attrs, attr{name: name, value: &types.AttributeValueMemberS{Value: *value}, empty: *value == ""})
		}
	}
	if patch.Mode != nil {
		v := string(*patch.Mode)
		addString("mode", &v)
	}
	if patch.Desired != nil {
		v := string(*patch.Desired)
		addString("desired", &v)
	}
	addString("start_cron", patch.StartCron)
	addString("stop_cron", patch.StopCron)
	addString("timezone", patch.Timezone)
	addString("tag_key", patch.TagKey)
	addString("tag_value", patch.TagValue)
	if patch.Types != nil {
		values := model.TypeNames(*patch.Types)
		attrs = append(attrs, attr{name: "types", value: &types.AttributeValueMemberSS{Value: values}, empty: len(values) == 0})
	}
	if len(attrs) == 0 {
		return nil
	}
	names := map[string]string{"#pk": "pk"}
	values := map[string]types.AttributeValue{}
	var sets, removes []string
	for i, attr := range attrs {
		n := fmt.Sprintf("#a%d", i)
		names[n] = attr.name
		if attr.empty {
			removes = append(removes, n)
			continue
		}
		v := fmt.Sprintf(":v%d", i)
		values[v] = attr.value
		sets = append(sets, n+" = "+v)
	}
	var expressions []string
	if len(sets) > 0 {
		expressions = append(expressions, "SET "+strings.Join(sets, ", "))
	}
	if len(removes) > 0 {
		expressions = append(expressions, "REMOVE "+strings.Join(removes, ", "))
	}
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.table, Key: marshalKey(groupKey(name)), UpdateExpression: aws.String(strings.Join(expressions, " ")),
		ConditionExpression: aws.String("attribute_exists(#pk)"), ExpressionAttributeNames: names, ExpressionAttributeValues: values,
	})
	if isConditionalCheckFailed(err) {
		return fmt.Errorf("%w: %s", ErrGroupNotFound, name)
	}
	return err
}

func (s *Store) GetOverride(ctx context.Context, group string, now time.Time) (*model.Override, error) {
	raw, err := s.get(ctx, overrideKey(group))
	if err != nil || raw == nil {
		return nil, err
	}
	var item overrideItem
	if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
		return nil, fmt.Errorf("unmarshal override %s: %w", group, err)
	}
	override := item.override()
	if override.ExpiresAt <= now.Unix() {
		return nil, nil
	}
	if override.Desired.Validate() != nil {
		return nil, fmt.Errorf("%s: override desired must be running|stopped", group)
	}
	return &override, nil
}

func (s *Store) PutOverride(ctx context.Context, group string, override model.Override) error {
	k := overrideKey(group)
	_, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.table, Item: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: k.PK}, "sk": &types.AttributeValueMemberS{Value: k.SK},
		"desired":    &types.AttributeValueMemberS{Value: string(override.Desired)},
		"expires_at": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", override.ExpiresAt)},
	}})
	return err
}

func (s *Store) GetStatus(ctx context.Context, resourceID string) (model.Status, error) {
	raw, err := s.get(ctx, statusKey(resourceID))
	if err != nil || raw == nil {
		return model.Status{}, err
	}
	var item statusItem
	if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
		return model.Status{}, fmt.Errorf("unmarshal status %s: %w", resourceID, err)
	}
	return item.status(), nil
}

// GetStatusesは指定されたstatusを100件単位の一貫性の強いBatchGetItemで取得する。
func (s *Store) GetStatuses(ctx context.Context, resourceIDs []string) (map[string]StatusRecord, error) {
	out := make(map[string]StatusRecord, len(resourceIDs))
	seen := map[string]struct{}{}
	keys := make([]itemKey, 0, len(resourceIDs))
	for _, id := range resourceIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		keys = append(keys, statusKey(id))
	}
	for start := 0; start < len(keys); start += 100 {
		end := min(start+100, len(keys))
		raws, err := s.batchGet(ctx, keys[start:end])
		if err != nil {
			return nil, fmt.Errorf("batch get statuses: %w", err)
		}
		for _, raw := range raws {
			pk, sk, ok := rawKey(raw)
			if !ok || !strings.HasPrefix(pk, statusPKPrefix) || sk != currentSK {
				continue
			}
			id := strings.TrimPrefix(pk, statusPKPrefix)
			var item statusItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				out[id] = StatusRecord{Err: fmt.Errorf("unmarshal status %s: %w", id, err)}
				continue
			}
			out[id] = StatusRecord{Status: item.status()}
		}
	}
	return out, nil
}

func (s *Store) UpdateStatus(ctx context.Context, resourceID string, patch StatusPatch) error {
	return s.updateStatus(ctx, resourceID, patch, "", nil, nil)
}

func (s *Store) updateStatus(
	ctx context.Context,
	resourceID string,
	patch StatusPatch,
	condition string,
	conditionNames map[string]string,
	conditionValues map[string]types.AttributeValue,
) error {
	attrs := patch.attributes()
	if len(attrs) == 0 {
		return nil
	}
	names := make(map[string]string, len(attrs)+len(conditionNames))
	values := make(map[string]types.AttributeValue, len(attrs)+len(conditionValues))
	for name, value := range conditionNames {
		names[name] = value
	}
	for name, value := range conditionValues {
		values[name] = value
	}
	terms := make([]string, 0, len(attrs))
	for i, attr := range attrs {
		n, v := fmt.Sprintf("#a%d", i), fmt.Sprintf(":v%d", i)
		names[n] = attr.name
		values[v] = &types.AttributeValueMemberS{Value: attr.value}
		terms = append(terms, n+" = "+v)
	}
	in := &dynamodb.UpdateItemInput{
		TableName: &s.table, Key: marshalKey(statusKey(resourceID)), UpdateExpression: aws.String("SET " + strings.Join(terms, ", ")),
		ExpressionAttributeNames: names, ExpressionAttributeValues: values,
	}
	if condition != "" {
		in.ConditionExpression = aws.String(condition)
	}
	_, err := s.db.UpdateItem(ctx, in)
	return err
}

// AcquireLease は期限切れのときだけ全体 reconcile のリースを取得する。
func (s *Store) AcquireLease(ctx context.Context, owner string, now, expiresAt time.Time) (bool, error) {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           &s.table,
		Key:                 marshalKey(reconcileLockKey()),
		UpdateExpression:    aws.String("SET #owner = :owner, #expires_at = :expires_at"),
		ConditionExpression: aws.String("attribute_not_exists(#pk) OR #expires_at < :now"),
		ExpressionAttributeNames: map[string]string{
			"#pk": "pk", "#owner": "owner", "#expires_at": "expires_at",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":owner":      &types.AttributeValueMemberS{Value: owner},
			":now":        &types.AttributeValueMemberN{Value: fmt.Sprint(now.Unix())},
			":expires_at": &types.AttributeValueMemberN{Value: fmt.Sprint(expiresAt.Unix())},
		},
	})
	if err == nil {
		return true, nil
	}
	if isConditionalCheckFailed(err) {
		return false, nil
	}
	return false, fmt.Errorf("acquire reconcile lease: %w", err)
}

// ReleaseLease は所有者が一致するリースだけを削除する。
func (s *Store) ReleaseLease(ctx context.Context, owner string) error {
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName:                &s.table,
		Key:                      marshalKey(reconcileLockKey()),
		ConditionExpression:      aws.String("#owner = :owner"),
		ExpressionAttributeNames: map[string]string{"#owner": "owner"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":owner": &types.AttributeValueMemberS{Value: owner},
		},
	})
	if err != nil {
		return fmt.Errorf("release reconcile lease: %w", err)
	}
	return nil
}

func isConditionalCheckFailed(err error) bool {
	var target *types.ConditionalCheckFailedException
	return errors.As(err, &target)
}

func (s *Store) DeleteGroup(ctx context.Context, name string) error {
	return s.delete(ctx, groupKey(name))
}
func (s *Store) DeleteOverride(ctx context.Context, name string) error {
	return s.delete(ctx, overrideKey(name))
}
func (s *Store) DeleteGroupStatus(ctx context.Context, name string) error {
	return s.delete(ctx, groupStatusKey(name))
}
func (s *Store) DeleteStatus(ctx context.Context, resourceID string) error {
	return s.delete(ctx, statusKey(resourceID))
}

func StatusPK(resourceID string) string { return statusKey(resourceID).PK }
func StatusSK() string                  { return currentSK }
func GroupPK(string) string             { return configPK }
func GroupSK(name string) string        { return groupKey(name).SK }
func OverridePK(string) string          { return configPK }
func OverrideSK(name string) string     { return overrideKey(name).SK }
func GroupStatusPK(name string) string  { return groupStatusKey(name).PK }
func GroupStatusSK() string             { return currentSK }

func (s *Store) delete(ctx context.Context, key itemKey) error {
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: &s.table, Key: marshalKey(key)})
	return err
}

func (s *Store) get(ctx context.Context, key itemKey) (map[string]types.AttributeValue, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: &s.table, Key: marshalKey(key), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", key.PK, key.SK, err)
	}
	return out.Item, nil
}

const batchGetMaxAttempts = 8

func (s *Store) batchGet(ctx context.Context, keys []itemKey) ([]map[string]types.AttributeValue, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	request := types.KeysAndAttributes{ConsistentRead: aws.Bool(true), Keys: make([]map[string]types.AttributeValue, 0, len(keys))}
	for _, key := range keys {
		request.Keys = append(request.Keys, marshalKey(key))
	}
	var raws []map[string]types.AttributeValue
	for attempt := 0; attempt < batchGetMaxAttempts; attempt++ {
		out, err := s.db.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: map[string]types.KeysAndAttributes{s.table: request}})
		if err != nil {
			return nil, err
		}
		raws = append(raws, out.Responses[s.table]...)
		unprocessed, ok := out.UnprocessedKeys[s.table]
		if !ok || len(unprocessed.Keys) == 0 {
			return raws, nil
		}
		if attempt == batchGetMaxAttempts-1 {
			break
		}
		request = unprocessed
		if err := s.batchGetBackoff.Wait(ctx, attempt); err != nil {
			return nil, fmt.Errorf("wait to retry batch get: %w", err)
		}
	}
	return nil, fmt.Errorf("batch get left unprocessed keys after %d attempts", batchGetMaxAttempts)
}

func marshalKey(key itemKey) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: key.PK}, "sk": &types.AttributeValueMemberS{Value: key.SK}}
}

func rawKey(item map[string]types.AttributeValue) (string, string, bool) {
	pk, pok := item["pk"].(*types.AttributeValueMemberS)
	sk, sok := item["sk"].(*types.AttributeValueMemberS)
	if !pok || !sok {
		return "", "", false
	}
	return pk.Value, sk.Value, true
}
