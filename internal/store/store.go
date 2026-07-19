// Package store is the DynamoDB access layer. config# items are read-only for the reconciler; status# items are owned by it. The CLI additionally writes config# and override# items.
package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"cheapskate/internal/model"
)

// API is the subset of the DynamoDB client the store uses.
type API interface {
	Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
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

// ListConfigs returns every config# item, sorted by pk.
func (s *Store) ListConfigs(ctx context.Context) ([]model.ConfigItem, error) {
	var items []model.ConfigItem
	var startKey map[string]types.AttributeValue
	for {
		out, err := s.db.Scan(ctx, &dynamodb.ScanInput{
			TableName:                 &s.table,
			FilterExpression:          aws.String("begins_with(pk, :p)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":p": &types.AttributeValueMemberS{Value: model.ConfigPrefix}},
			ExclusiveStartKey:         startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("scan configs: %w", err)
		}
		var page []model.ConfigItem
		if err := attributevalue.UnmarshalListOfMaps(out.Items, &page); err != nil {
			return nil, fmt.Errorf("unmarshal configs: %w", err)
		}
		items = append(items, page...)
		if out.LastEvaluatedKey == nil {
			break
		}
		startKey = out.LastEvaluatedKey
	}
	sort.Slice(items, func(i, j int) bool { return items[i].PK < items[j].PK })
	return items, nil
}

// ScanRow is one resource_id's config/override/status joined from a single ScanAll pass.
type ScanRow struct {
	ResourceID string
	Config     model.ConfigItem
	HasConfig  bool // false when this resource_id has an override or status item but no config (orphaned data)
	Override   *model.Override
	Status     model.Status
	Err        error // set when this resource_id's override or status item failed to unmarshal/validate; Config/Override/Status may still be partially populated
}

// ScanAll performs a single paginated Scan over the whole table and joins config/override/status items by resource_id, replacing the config-then-GetOverride-then-GetStatus N+1 GetItem pattern (C-1) with one Scan pass.
//
// A malformed override or status item is recorded as that resource_id's Err rather than aborting the scan (B-5) — one bad row must not take down the list for everyone else.
func (s *Store) ScanAll(ctx context.Context, now time.Time) ([]ScanRow, error) {
	var raws []map[string]types.AttributeValue
	var startKey map[string]types.AttributeValue
	for {
		out, err := s.db.Scan(ctx, &dynamodb.ScanInput{TableName: &s.table, ExclusiveStartKey: startKey})
		if err != nil {
			return nil, fmt.Errorf("scan all: %w", err)
		}
		raws = append(raws, out.Items...)
		if out.LastEvaluatedKey == nil {
			break
		}
		startKey = out.LastEvaluatedKey
	}

	rows := map[string]*ScanRow{}
	var order []string
	rowFor := func(resourceID string) *ScanRow {
		r, ok := rows[resourceID]
		if !ok {
			r = &ScanRow{ResourceID: resourceID}
			rows[resourceID] = r
			order = append(order, resourceID)
		}
		return r
	}

	for _, raw := range raws {
		pkAttr, ok := raw["pk"].(*types.AttributeValueMemberS)
		if !ok {
			continue
		}
		pk := pkAttr.Value
		switch {
		case strings.HasPrefix(pk, model.ConfigPrefix):
			resourceID := strings.TrimPrefix(pk, model.ConfigPrefix)
			var item model.ConfigItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				rowFor(resourceID).Err = fmt.Errorf("unmarshal config %s: %w", resourceID, err)
				continue
			}
			r := rowFor(resourceID)
			r.Config, r.HasConfig = item, true
		case strings.HasPrefix(pk, model.OverridePrefix):
			resourceID := strings.TrimPrefix(pk, model.OverridePrefix)
			var o model.Override
			if err := attributevalue.UnmarshalMap(raw, &o); err != nil {
				rowFor(resourceID).Err = fmt.Errorf("unmarshal override %s: %w", resourceID, err)
				continue
			}
			if o.ExpiresAt <= now.Unix() {
				continue // expired; DynamoDB TTL deletion is lazy, so enforce it here as GetOverride does
			}
			if o.Desired != model.DesiredRunning && o.Desired != model.DesiredStopped {
				rowFor(resourceID).Err = fmt.Errorf("%s: override desired must be running|stopped", resourceID)
				continue
			}
			rowFor(resourceID).Override = &o
		case strings.HasPrefix(pk, model.StatusPrefix):
			resourceID := strings.TrimPrefix(pk, model.StatusPrefix)
			var st model.Status
			if err := attributevalue.UnmarshalMap(raw, &st); err != nil {
				rowFor(resourceID).Err = fmt.Errorf("unmarshal status %s: %w", resourceID, err)
				continue
			}
			rowFor(resourceID).Status = st
		}
	}

	sort.Strings(order)
	result := make([]ScanRow, 0, len(order))
	for _, id := range order {
		result = append(result, *rows[id])
	}
	return result, nil
}

// GetConfig returns the config item for a resource, or nil when unregistered.
func (s *Store) GetConfig(ctx context.Context, resourceID string) (*model.ConfigItem, error) {
	raw, err := s.get(ctx, model.ConfigPrefix+resourceID)
	if err != nil || raw == nil {
		return nil, err
	}
	var item model.ConfigItem
	if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
		return nil, fmt.Errorf("unmarshal config %s: %w", resourceID, err)
	}
	return &item, nil
}

// PutConfig writes a config item (CLI only; the reconciler never calls this).
func (s *Store) PutConfig(ctx context.Context, item model.ConfigItem) error {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.table, Item: av})
	return err
}

// GetOverride returns the unexpired override for a resource, or nil. DynamoDB TTL deletion is lazy; the expiry is enforced here.
func (s *Store) GetOverride(ctx context.Context, resourceID string, now time.Time) (*model.Override, error) {
	raw, err := s.get(ctx, model.OverridePrefix+resourceID)
	if err != nil || raw == nil {
		return nil, err
	}
	var o model.Override
	if err := attributevalue.UnmarshalMap(raw, &o); err != nil {
		return nil, fmt.Errorf("unmarshal override %s: %w", resourceID, err)
	}
	if o.ExpiresAt <= now.Unix() {
		return nil, nil
	}
	if o.Desired != model.DesiredRunning && o.Desired != model.DesiredStopped {
		return nil, fmt.Errorf("%s: override desired must be running|stopped", resourceID)
	}
	return &o, nil
}

// PutOverride writes an override item with its TTL attribute (CLI only).
func (s *Store) PutOverride(ctx context.Context, resourceID string, o model.Override) error {
	_, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &s.table,
		Item: map[string]types.AttributeValue{
			"pk":         &types.AttributeValueMemberS{Value: model.OverridePrefix + resourceID},
			"desired":    &types.AttributeValueMemberS{Value: o.Desired},
			"expires_at": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", o.ExpiresAt)},
		},
	})
	return err
}

// GetStatus returns the status item; a zero Status when absent.
func (s *Store) GetStatus(ctx context.Context, resourceID string) (model.Status, error) {
	raw, err := s.get(ctx, model.StatusPrefix+resourceID)
	if err != nil || raw == nil {
		return model.Status{}, err
	}
	var st model.Status
	if err := attributevalue.UnmarshalMap(raw, &st); err != nil {
		return model.Status{}, fmt.Errorf("unmarshal status %s: %w", resourceID, err)
	}
	return st, nil
}

// SavedStateAttrs maps a target's SavedState to the status-item attribute names PutStatus expects. A nil field is omitted so PutStatus leaves the existing stored value alone; a nil SavedState yields an empty map.
func SavedStateAttrs(s *model.SavedState) map[string]any {
	attrs := map[string]any{}
	if s == nil {
		return attrs
	}
	if s.DesiredCount != nil {
		attrs["saved_desired_count"] = *s.DesiredCount
	}
	if s.ScalingMin != nil {
		attrs["saved_scaling_min"] = *s.ScalingMin
	}
	if s.ScalingMax != nil {
		attrs["saved_scaling_max"] = *s.ScalingMax
	}
	return attrs
}

// PutStatus SETs the given attributes on the status item, skipping nil values.
func (s *Store) PutStatus(ctx context.Context, resourceID string, attrs map[string]any) error {
	names := map[string]string{}
	values := map[string]types.AttributeValue{}
	var terms []string
	i := 0
	for k, v := range attrs {
		if v == nil {
			continue
		}
		av, err := attributevalue.Marshal(v)
		if err != nil {
			return fmt.Errorf("marshal status attr %s: %w", k, err)
		}
		n, p := fmt.Sprintf("#a%d", i), fmt.Sprintf(":v%d", i)
		names[n], values[p] = k, av
		terms = append(terms, n+" = "+p)
		i++
	}
	if len(terms) == 0 {
		return nil
	}
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &s.table,
		Key:                       key(model.StatusPrefix + resourceID),
		UpdateExpression:          aws.String("SET " + strings.Join(terms, ", ")),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
	})
	return err
}

// Delete removes the item with the given pk (CLI only). Deleting a missing item is not an error.
func (s *Store) Delete(ctx context.Context, pk string) error {
	_, err := s.db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: &s.table, Key: key(pk)})
	return err
}

func (s *Store) get(ctx context.Context, pk string) (map[string]types.AttributeValue, error) {
	out, err := s.db.GetItem(ctx, &dynamodb.GetItemInput{TableName: &s.table, Key: key(pk)})
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", pk, err)
	}
	if out.Item == nil {
		return nil, nil
	}
	return out.Item, nil
}

func key(pk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: pk}}
}
