// Package dynafake is an in-memory fake of the DynamoDB API subset used by the store, for unit tests. It implements just the expression shapes the store emits (begins_with scan filter, SET update expressions), plus error injection and Scan paging to exercise the store's retry/pagination paths. It deliberately stays this narrow — no condition expressions, no other operators — since the store never emits them; keep it that way rather than growing it into a full DynamoDB emulator.
package dynafake

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type Fake struct {
	mu           sync.Mutex
	items        map[string]map[string]types.AttributeValue
	fail         map[string]error
	scanPageSize int
}

func New() *Fake {
	return &Fake{items: map[string]map[string]types.AttributeValue{}}
}

// FailOn makes the next matching call to the named operation ("get", "put", "update", "delete", "scan") return err. For get/put/update/delete, pk scopes it to that key; pass "" to match any key for that op. It fires once, then clears itself, so a test can inject a single failure without affecting later calls (e.g. the retry after it).
func (f *Fake) FailOn(op, pk string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]error{}
	}
	f.fail[op+"|"+pk] = err
}

// SetScanPageSize makes Scan return at most n items per call, reporting LastEvaluatedKey so the caller must page through the rest. 0 (the default) means unlimited (one page).
func (f *Fake) SetScanPageSize(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanPageSize = n
}

// takeFailure consumes and returns an injected failure for (op, pk), preferring a key-specific one over the op-wide "" one.
func (f *Fake) takeFailure(op, pk string) error {
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
func (f *Fake) Seed(item map[string]types.AttributeValue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[pkOf(item)] = item
}

// Item returns a stored item (nil when absent).
func (f *Fake) Item(pk string) map[string]types.AttributeValue {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.items[pk]
}

func (f *Fake) Scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeFailure("scan", ""); err != nil {
		return nil, err
	}
	prefix := ""
	if in.FilterExpression != nil {
		if !strings.HasPrefix(*in.FilterExpression, "begins_with(pk, ") {
			return nil, fmt.Errorf("dynafake: unsupported filter %q", *in.FilterExpression)
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

func (f *Fake) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeFailure("get", pkOf(in.Key)); err != nil {
		return nil, err
	}
	return &dynamodb.GetItemOutput{Item: f.items[pkOf(in.Key)]}, nil
}

func (f *Fake) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.takeFailure("put", pkOf(in.Item)); err != nil {
		return nil, err
	}
	f.items[pkOf(in.Item)] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *Fake) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
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
			return nil, fmt.Errorf("dynafake: unsupported update term %q", term)
		}
		attr, ok := in.ExpressionAttributeNames[name]
		if !ok {
			return nil, fmt.Errorf("dynafake: unbound name %q", name)
		}
		av, ok := in.ExpressionAttributeValues[value]
		if !ok {
			return nil, fmt.Errorf("dynafake: unbound value %q", value)
		}
		item[attr] = av
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *Fake) DeleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
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
