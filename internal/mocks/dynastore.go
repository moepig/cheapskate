// DynaStore is a hand-written in-memory backing for a generated MockStoreAPI. It implements just
// the expression shapes the store emits (begins_with scan filter, SET update expressions), plus
// error injection and Scan paging to exercise the store's retry/pagination paths. It deliberately
// stays this narrow — no condition expressions beyond what store.CreateMember needs, no other
// operators — since the store never emits more than that; keep it that way rather than growing it
// into a full DynamoDB emulator. This is a direct port of the former internal/dynafake package,
// wired to a MockStoreAPI so gomock is the seam instead of a hand-rolled interface implementation.
package mocks

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"go.uber.org/mock/gomock"
)

type DynaStore struct {
	mu           sync.Mutex
	items        map[string]map[string]types.AttributeValue
	fail         map[string]error
	scanPageSize int
}

// NewDynaStore returns a generated MockStoreAPI whose Scan/GetItem/PutItem/UpdateItem/DeleteItem
// are backed by an in-memory table, plus the state handle for seeding, inspection, and failure
// injection.
func NewDynaStore(ctrl *gomock.Controller) (*MockStoreAPI, *DynaStore) {
	st := &DynaStore{items: map[string]map[string]types.AttributeValue{}}
	m := NewMockStoreAPI(ctrl)
	m.EXPECT().Scan(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.scan)
	m.EXPECT().GetItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.getItem)
	m.EXPECT().PutItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.putItem)
	m.EXPECT().UpdateItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.updateItem)
	m.EXPECT().DeleteItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.deleteItem)
	return m, st
}

// FailOn makes the next matching call to the named operation ("get", "put", "update", "delete", "scan") return err. For get/put/update/delete, pk scopes it to that key; pass "" to match any key for that op. It fires once, then clears itself, so a test can inject a single failure without affecting later calls (e.g. the retry after it).
func (f *DynaStore) FailOn(op, pk string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]error{}
	}
	f.fail[op+"|"+pk] = err
}

// SetScanPageSize makes Scan return at most n items per call, reporting LastEvaluatedKey so the caller must page through the rest. 0 (the default) means unlimited (one page).
func (f *DynaStore) SetScanPageSize(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanPageSize = n
}

// takeFailure consumes and returns an injected failure for (op, pk), preferring a key-specific one over the op-wide "" one.
func (f *DynaStore) takeFailure(op, pk string) error {
	if f.fail == nil {
		return nil
	}
	if err, ok := f.fail[op+"|"+pk]; ok {
		delete(f.fail, op+"|"+pk)
		return err
	}
	if err, ok := f.fail[op+"|"]; ok {
		delete(f.fail, op+"|")
		return err
	}
	return nil
}

// Seed stores an item keyed by its pk attribute.
func (f *DynaStore) Seed(item map[string]types.AttributeValue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[pkOf(item)] = item
}

// Item returns a stored item (nil when absent).
func (f *DynaStore) Item(pk string) map[string]types.AttributeValue {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.items[pk]
}

func (f *DynaStore) scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeFailure("scan", ""); err != nil {
		return nil, err
	}
	prefix := ""
	if in.FilterExpression != nil {
		if !strings.HasPrefix(*in.FilterExpression, "begins_with(pk, ") {
			return nil, fmt.Errorf("dynastore: unsupported filter %q", *in.FilterExpression)
		}
		prefix = in.ExpressionAttributeValues[":p"].(*types.AttributeValueMemberS).Value
	}
	var pks []string
	for pk := range f.items {
		if strings.HasPrefix(pk, prefix) {
			pks = append(pks, pk)
		}
	}
	sort.Strings(pks)

	if in.ExclusiveStartKey != nil {
		after := pkOf(in.ExclusiveStartKey)
		i := sort.SearchStrings(pks, after)
		if i < len(pks) && pks[i] == after {
			i++
		}
		pks = pks[i:]
	}

	out := &dynamodb.ScanOutput{}
	if n := f.scanPageSize; n > 0 && len(pks) > n {
		last := pks[n-1]
		pks = pks[:n]
		out.LastEvaluatedKey = map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: last}}
	}
	for _, pk := range pks {
		out.Items = append(out.Items, f.items[pk])
	}
	return out, nil
}

func (f *DynaStore) getItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeFailure("get", pkOf(in.Key)); err != nil {
		return nil, err
	}
	return &dynamodb.GetItemOutput{Item: f.items[pkOf(in.Key)]}, nil
}

func (f *DynaStore) putItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pk := pkOf(in.Item)
	if err := f.takeFailure("put", pk); err != nil {
		return nil, err
	}
	if in.ConditionExpression != nil {
		switch *in.ConditionExpression {
		case "attribute_not_exists(pk)":
			if _, exists := f.items[pk]; exists {
				return nil, &types.ConditionalCheckFailedException{}
			}
		default:
			return nil, fmt.Errorf("dynastore: unsupported condition %q", *in.ConditionExpression)
		}
	}
	f.items[pk] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *DynaStore) updateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pk := pkOf(in.Key)
	if err := f.takeFailure("update", pk); err != nil {
		return nil, err
	}
	item := f.items[pk]
	if item == nil {
		item = map[string]types.AttributeValue{"pk": in.Key["pk"]}
		f.items[pk] = item
	}
	expr := strings.TrimPrefix(*in.UpdateExpression, "SET ")
	for _, term := range strings.Split(expr, ", ") {
		name, value, found := strings.Cut(term, " = ")
		if !found {
			return nil, fmt.Errorf("dynastore: unsupported update term %q", term)
		}
		attr, ok := in.ExpressionAttributeNames[name]
		if !ok {
			return nil, fmt.Errorf("dynastore: unbound name %q", name)
		}
		av, ok := in.ExpressionAttributeValues[value]
		if !ok {
			return nil, fmt.Errorf("dynastore: unbound value %q", value)
		}
		item[attr] = av
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *DynaStore) deleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeFailure("delete", pkOf(in.Key)); err != nil {
		return nil, err
	}
	delete(f.items, pkOf(in.Key))
	return &dynamodb.DeleteItemOutput{}, nil
}

func pkOf(item map[string]types.AttributeValue) string {
	if s, ok := item["pk"].(*types.AttributeValueMemberS); ok {
		return s.Value
	}
	return ""
}
