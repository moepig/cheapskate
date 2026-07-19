// Package store is the DynamoDB access layer. tag# and member# items are read-only for the
// reconciler; status# items are owned by it. The CLI additionally writes tag#, member#, and
// override# items.
package store

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

	"cheapskate/internal/model"
)

//go:generate go tool mockgen -destination ../mocks/store.go -package mocks -mock_names API=MockStoreAPI cheapskate/internal/store API

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

// MemberRow is one member's item joined with its status from a single ScanAll pass.
type MemberRow struct {
	ResourceID string
	Member     model.MemberItem
	Status     model.Status
}

// TagRow is one tag's config joined with its override and members from a single ScanAll pass.
type TagRow struct {
	Name     string
	Tag      model.TagItem
	HasTag   bool // false when this tag name has members/override/status but no tag# item (orphaned data)
	Override *model.Override
	Members  []MemberRow // sorted by ResourceID
	Err      error       // set when this tag's override or a member/status item failed to unmarshal/validate; other fields may still be partially populated
}

// ScanAll performs a single paginated Scan over the whole table and joins tag/member/override/status items by tag name (and, for members and their statuses, by resource_id), replacing a tag-then-GetOverride-then-per-member-GetStatus N+1 GetItem pattern with one Scan pass.
//
// A malformed override, member, or status item is recorded as that tag row's Err rather than aborting the scan (B-5) — one bad row must not take down the list for everyone else.
func (s *Store) ScanAll(ctx context.Context, now time.Time) ([]TagRow, error) {
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

	rows := map[string]*TagRow{}
	var order []string
	rowFor := func(tag string) *TagRow {
		r, ok := rows[tag]
		if !ok {
			r = &TagRow{Name: tag}
			rows[tag] = r
			order = append(order, tag)
		}
		return r
	}

	// Members and statuses are joined by resource_id, which requires knowing each member's tag
	// before it can be attached to a TagRow; statuses may be scanned before or after their
	// member, so both are staged and resolved in a second pass.
	memberByResourceID := map[string]model.MemberItem{}
	memberOrder := map[string][]string{} // tag -> resourceIDs, in first-seen order
	statusByResourceID := map[string]model.Status{}
	memberErrByResourceID := map[string]error{}

	for _, raw := range raws {
		pkAttr, ok := raw["pk"].(*types.AttributeValueMemberS)
		if !ok {
			continue
		}
		pk := pkAttr.Value
		switch {
		case strings.HasPrefix(pk, model.TagPrefix):
			name := strings.TrimPrefix(pk, model.TagPrefix)
			var item model.TagItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				rowFor(name).Err = fmt.Errorf("unmarshal tag %s: %w", name, err)
				continue
			}
			r := rowFor(name)
			r.Tag, r.HasTag = item, true
		case strings.HasPrefix(pk, model.MemberPrefix):
			resourceID := strings.TrimPrefix(pk, model.MemberPrefix)
			var item model.MemberItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				// The failure may have hit the "tag" field itself, so which TagRow this member
				// belongs to is unknowable; park the error on the untagged/"" row instead of
				// dropping it. That row's HasTag is always false, so callers that skip orphaned
				// rows (ops.List, the reconciler) already ignore it, exactly like any other
				// orphan — the error is only visible to a caller that inspects ScanAll directly.
				rowFor("").Err = fmt.Errorf("unmarshal member %s: %w", resourceID, err)
				continue
			}
			memberByResourceID[resourceID] = item
			memberOrder[item.Tag] = append(memberOrder[item.Tag], resourceID)
			rowFor(item.Tag) // ensure a row exists even if this tag has no tag# item
		case strings.HasPrefix(pk, model.OverridePrefix):
			name := strings.TrimPrefix(pk, model.OverridePrefix)
			var o model.Override
			if err := attributevalue.UnmarshalMap(raw, &o); err != nil {
				rowFor(name).Err = fmt.Errorf("unmarshal override %s: %w", name, err)
				continue
			}
			if o.ExpiresAt <= now.Unix() {
				continue // expired; DynamoDB TTL deletion is lazy, so enforce it here as GetOverride does
			}
			if o.Desired != model.DesiredRunning && o.Desired != model.DesiredStopped {
				rowFor(name).Err = fmt.Errorf("%s: override desired must be running|stopped", name)
				continue
			}
			rowFor(name).Override = &o
		case strings.HasPrefix(pk, model.StatusPrefix):
			resourceID := strings.TrimPrefix(pk, model.StatusPrefix)
			var st model.Status
			if err := attributevalue.UnmarshalMap(raw, &st); err != nil {
				memberErrByResourceID[resourceID] = fmt.Errorf("unmarshal status %s: %w", resourceID, err)
				continue
			}
			statusByResourceID[resourceID] = st
		}
	}

	for tag, resourceIDs := range memberOrder {
		sort.Strings(resourceIDs)
		r := rowFor(tag)
		for _, resourceID := range resourceIDs {
			if err, ok := memberErrByResourceID[resourceID]; ok {
				r.Err = err
				continue
			}
			r.Members = append(r.Members, MemberRow{
				ResourceID: resourceID,
				Member:     memberByResourceID[resourceID],
				Status:     statusByResourceID[resourceID],
			})
		}
	}

	sort.Strings(order)
	result := make([]TagRow, 0, len(order))
	for _, name := range order {
		result = append(result, *rows[name])
	}
	return result, nil
}

// GetTag returns the tag item, or nil when unregistered.
func (s *Store) GetTag(ctx context.Context, name string) (*model.TagItem, error) {
	raw, err := s.get(ctx, model.TagPrefix+name)
	if err != nil || raw == nil {
		return nil, err
	}
	var item model.TagItem
	if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
		return nil, fmt.Errorf("unmarshal tag %s: %w", name, err)
	}
	return &item, nil
}

// PutTag writes a tag item (CLI only; the reconciler never calls this).
func (s *Store) PutTag(ctx context.Context, item model.TagItem) error {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("marshal tag: %w", err)
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.table, Item: av})
	return err
}

// GetMember returns the member item for a resource, or nil when it has no tag.
func (s *Store) GetMember(ctx context.Context, resourceID string) (*model.MemberItem, error) {
	raw, err := s.get(ctx, model.MemberPrefix+resourceID)
	if err != nil || raw == nil {
		return nil, err
	}
	var item model.MemberItem
	if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
		return nil, fmt.Errorf("unmarshal member %s: %w", resourceID, err)
	}
	return &item, nil
}

// PutMember writes a member item, overwriting any existing membership for that resource_id (CLI only; used to update an existing member of the same tag).
func (s *Store) PutMember(ctx context.Context, item model.MemberItem) error {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("marshal member: %w", err)
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.table, Item: av})
	return err
}

// ErrMemberExists is returned by CreateMember when the resource already belongs to a tag.
var ErrMemberExists = errors.New("member already exists")

// CreateMember writes a member item only if the resource has no existing membership, enforcing
// the one-resource-one-tag invariant atomically. Returns ErrMemberExists on conflict.
func (s *Store) CreateMember(ctx context.Context, item model.MemberItem) error {
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("marshal member: %w", err)
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &s.table,
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return ErrMemberExists
		}
		return err
	}
	return nil
}

// ListMembers returns every member of the given tag, sorted by resource_id.
func (s *Store) ListMembers(ctx context.Context, tag string) ([]model.MemberItem, error) {
	var items []model.MemberItem
	var startKey map[string]types.AttributeValue
	for {
		out, err := s.db.Scan(ctx, &dynamodb.ScanInput{
			TableName:                 &s.table,
			FilterExpression:          aws.String("begins_with(pk, :p)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":p": &types.AttributeValueMemberS{Value: model.MemberPrefix}},
			ExclusiveStartKey:         startKey,
		})
		if err != nil {
			return nil, fmt.Errorf("scan members: %w", err)
		}
		var page []model.MemberItem
		if err := attributevalue.UnmarshalListOfMaps(out.Items, &page); err != nil {
			return nil, fmt.Errorf("unmarshal members: %w", err)
		}
		for _, item := range page {
			if item.Tag == tag {
				items = append(items, item)
			}
		}
		if out.LastEvaluatedKey == nil {
			break
		}
		startKey = out.LastEvaluatedKey
	}
	sort.Slice(items, func(i, j int) bool { return items[i].PK < items[j].PK })
	return items, nil
}

// GetOverride returns the unexpired override for a tag, or nil. DynamoDB TTL deletion is lazy; the expiry is enforced here.
func (s *Store) GetOverride(ctx context.Context, tag string, now time.Time) (*model.Override, error) {
	raw, err := s.get(ctx, model.OverridePrefix+tag)
	if err != nil || raw == nil {
		return nil, err
	}
	var o model.Override
	if err := attributevalue.UnmarshalMap(raw, &o); err != nil {
		return nil, fmt.Errorf("unmarshal override %s: %w", tag, err)
	}
	if o.ExpiresAt <= now.Unix() {
		return nil, nil
	}
	if o.Desired != model.DesiredRunning && o.Desired != model.DesiredStopped {
		return nil, fmt.Errorf("%s: override desired must be running|stopped", tag)
	}
	return &o, nil
}

// PutOverride writes an override item with its TTL attribute (CLI only).
func (s *Store) PutOverride(ctx context.Context, tag string, o model.Override) error {
	_, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &s.table,
		Item: map[string]types.AttributeValue{
			"pk":         &types.AttributeValueMemberS{Value: model.OverridePrefix + tag},
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
