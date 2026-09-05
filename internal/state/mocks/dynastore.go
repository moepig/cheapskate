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
	mu                  sync.Mutex
	items               map[string]map[string]types.AttributeValue
	fail                map[string]error
	calls               map[string]int
	queryPageSize       int
	queryConsistentRead bool
	getConsistentRead   bool
}

func NewDynaStore(controller *gomock.Controller) (*MockAPI, *DynaStore) {
	store := &DynaStore{items: map[string]map[string]types.AttributeValue{}, fail: map[string]error{}, calls: map[string]int{}}
	mock := NewMockAPI(controller)
	mock.EXPECT().Query(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(store.query)
	mock.EXPECT().GetItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(store.getItem)
	mock.EXPECT().PutItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(store.putItem)
	mock.EXPECT().UpdateItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(store.updateItem)
	mock.EXPECT().DeleteItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(store.deleteItem)
	return mock, store
}

func (store *DynaStore) Calls(operation string) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.calls[operation]
}

func (store *DynaStore) FailOn(operation, key string, err error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.fail[operation+"|"+key] = err
}

func (store *DynaStore) SetQueryPageSize(size int) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.queryPageSize = size
}

func (store *DynaStore) QueryConsistentRead() bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.queryConsistentRead
}

func (store *DynaStore) GetConsistentRead() bool {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.getConsistentRead
}

func (store *DynaStore) Seed(item map[string]types.AttributeValue) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.items[keyID(item)] = item
}

func (store *DynaStore) Item(pk string, sk ...string) map[string]types.AttributeValue {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: pk}}
	if len(sk) > 0 {
		key["sk"] = &types.AttributeValueMemberS{Value: sk[0]}
	}
	return store.items[keyID(key)]
}

func (store *DynaStore) query(_ context.Context, input *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls["query"]++
	store.queryConsistentRead = input.ConsistentRead != nil && *input.ConsistentRead
	if err := store.takeFailure("query", ""); err != nil {
		return nil, err
	}
	partition := input.ExpressionAttributeValues[":pk"].(*types.AttributeValueMemberS).Value
	keys := make([]string, 0)
	for key, item := range store.items {
		if pkOf(item) == partition {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(input.ExclusiveStartKey) != 0 {
		after := keyID(input.ExclusiveStartKey)
		position := sort.SearchStrings(keys, after)
		if position < len(keys) && keys[position] == after {
			position++
		}
		keys = keys[position:]
	}
	output := &dynamodb.QueryOutput{}
	if store.queryPageSize > 0 && len(keys) > store.queryPageSize {
		last := store.items[keys[store.queryPageSize-1]]
		keys = keys[:store.queryPageSize]
		output.LastEvaluatedKey = keyOf(last)
	}
	for _, key := range keys {
		output.Items = append(output.Items, store.items[key])
	}
	return output, nil
}

func (store *DynaStore) getItem(_ context.Context, input *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls["get"]++
	store.getConsistentRead = input.ConsistentRead != nil && *input.ConsistentRead
	if err := store.takeFailure("get", failureKey(input.Key)); err != nil {
		return nil, err
	}
	return &dynamodb.GetItemOutput{Item: store.items[keyID(input.Key)]}, nil
}

func (store *DynaStore) putItem(_ context.Context, input *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls["put"]++
	if err := store.takeFailure("put", failureKey(input.Item)); err != nil {
		return nil, err
	}
	if input.ConditionExpression != nil && store.items[keyID(input.Item)] != nil {
		return nil, &types.ConditionalCheckFailedException{}
	}
	store.items[keyID(input.Item)] = input.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (store *DynaStore) updateItem(_ context.Context, input *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls["update"]++
	if err := store.takeFailure("update", failureKey(input.Key)); err != nil {
		return nil, err
	}
	item := store.items[keyID(input.Key)]
	if !updateConditionMatches(item, input) {
		return nil, &types.ConditionalCheckFailedException{}
	}
	expression := *input.UpdateExpression
	setExpression, removeExpression := expression, ""
	if before, after, found := strings.Cut(expression, " REMOVE "); found {
		setExpression, removeExpression = before, after
	} else if after, found := strings.CutPrefix(expression, "REMOVE "); found {
		setExpression, removeExpression = "", after
	}
	for term := range strings.SplitSeq(strings.TrimPrefix(setExpression, "SET "), ", ") {
		if term == "" {
			continue
		}
		name, value, found := strings.Cut(term, " = ")
		if !found {
			return nil, fmt.Errorf("unsupported update term %q", term)
		}
		item[input.ExpressionAttributeNames[name]] = input.ExpressionAttributeValues[value]
	}
	for name := range strings.SplitSeq(removeExpression, ", ") {
		if name != "" {
			delete(item, input.ExpressionAttributeNames[name])
		}
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

func (store *DynaStore) deleteItem(_ context.Context, input *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls["delete"]++
	if err := store.takeFailure("delete", failureKey(input.Key)); err != nil {
		return nil, err
	}
	key := keyID(input.Key)
	if input.ConditionExpression != nil && store.items[key] == nil {
		return nil, &types.ConditionalCheckFailedException{}
	}
	delete(store.items, key)
	return &dynamodb.DeleteItemOutput{}, nil
}

func updateConditionMatches(item map[string]types.AttributeValue, input *dynamodb.UpdateItemInput) bool {
	if input.ConditionExpression == nil {
		return true
	}
	if item == nil || item[input.ExpressionAttributeNames["#pk"]] == nil {
		return false
	}
	if strings.Contains(*input.ConditionExpression, "attribute_exists(#start)") {
		return item[input.ExpressionAttributeNames["#start"]] != nil && item[input.ExpressionAttributeNames["#stop"]] != nil
	}
	return true
}

func (store *DynaStore) takeFailure(operation, key string) error {
	for _, candidate := range []string{operation + "|" + key, operation + "|"} {
		if err, ok := store.fail[candidate]; ok {
			delete(store.fail, candidate)
			return err
		}
	}
	return nil
}

func failureKey(item map[string]types.AttributeValue) string {
	pk, sk := pkOf(item), skOf(item)
	if pk == "CONFIG" && strings.HasPrefix(sk, "GROUP#") {
		return "group#" + strings.TrimPrefix(sk, "GROUP#")
	}
	return pk
}

func pkOf(item map[string]types.AttributeValue) string {
	if value, ok := item["pk"].(*types.AttributeValueMemberS); ok {
		return value.Value
	}
	return ""
}

func skOf(item map[string]types.AttributeValue) string {
	if value, ok := item["sk"].(*types.AttributeValueMemberS); ok {
		return value.Value
	}
	return ""
}

func keyID(item map[string]types.AttributeValue) string {
	return pkOf(item) + "\x00" + skOf(item)
}

func keyOf(item map[string]types.AttributeValue) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": item["pk"], "sk": item["sk"]}
}
