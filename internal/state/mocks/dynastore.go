// 生成されたMockAPIを裏で支える、手書きのインメモリ実装。
// Storeが発行するQuery、BatchGetItem、更新式、条件式だけを解釈する。
package mocks

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"go.uber.org/mock/gomock"
)

type DynaStore struct {
	mu                           sync.Mutex
	items                        map[string]map[string]types.AttributeValue
	fail                         map[string]error
	failNth                      map[string]delayedFailure
	calls                        map[string]int
	scanPageSize                 int
	unprocessedBatchGetResponses int
}

type delayedFailure struct {
	remaining int
	err       error
}

// Scan・GetItem・PutItem・UpdateItem・DeleteItem をインメモリのテーブルで裏打ちした、生成済みの MockAPI を返す
// 併せて、初期データ投入・内容確認・失敗注入のための状態ハンドルも返す
func NewDynaStore(ctrl *gomock.Controller) (*MockAPI, *DynaStore) {
	st := &DynaStore{items: map[string]map[string]types.AttributeValue{}, calls: map[string]int{}}
	m := NewMockAPI(ctrl)
	m.EXPECT().Scan(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.scan)
	m.EXPECT().Query(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.query)
	m.EXPECT().BatchGetItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.batchGetItem)
	m.EXPECT().GetItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.getItem)
	m.EXPECT().PutItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.putItem)
	m.EXPECT().UpdateItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.updateItem)
	m.EXPECT().DeleteItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.deleteItem)
	return m, st
}

// Callsは指定したDynamoDB操作の呼び出し回数を返す。
func (f *DynaStore) Calls(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[op]
}

// 指定した操作("get"・"put"・"update"・"delete"・"scan")の次に合致する呼び出しが err を返すようにする
// get/put/update/delete では pk がそのキーに限定し、"" を渡すとその操作の任意のキーに合致する
// 一度発火したら自ら解除されるので、後続の呼び出し(直後の再試行など)に影響を与えず単発の失敗を注入できる
func (f *DynaStore) FailOn(op, pk string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]error{}
	}
	f.fail[op+"|"+pk] = err
}

// FailOnNth は条件に合致する n 回目の呼び出しだけを失敗させる。
func (f *DynaStore) FailOnNth(op, pk string, n int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n < 1 {
		panic("DynaStore.FailOnNth: n must be at least 1")
	}
	if f.failNth == nil {
		f.failNth = map[string]delayedFailure{}
	}
	f.failNth[op+"|"+pk] = delayedFailure{remaining: n, err: err}
}

// Scan が 1 回あたり最大 n 件だけ返すようにし、LastEvaluatedKey を報告して呼び出し側に残りのページ送りを強いる
// 既定値の 0 は無制限(1 ページ)を意味する
func (f *DynaStore) SetScanPageSize(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanPageSize = n
}

// 続く n 回の BatchGetItem で、要求されたすべてのキーを UnprocessedKeys として返す。
func (f *DynaStore) SetBatchGetUnprocessedResponses(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unprocessedBatchGetResponses = n
}

// (op, pk) に注入された失敗を取り出して返す
// 操作全体に対する "" の指定よりも、キー個別の指定を優先する
func (f *DynaStore) takeFailure(op, pk string) error {
	keys := []string{op + "|" + pk, op + "|"}
	for _, key := range keys {
		if err, ok := f.fail[key]; ok {
			delete(f.fail, key)
			return err
		}
	}
	for _, key := range keys {
		failure, ok := f.failNth[key]
		if !ok {
			continue
		}
		failure.remaining--
		if failure.remaining == 0 {
			delete(f.failNth, key)
			return failure.err
		}
		f.failNth[key] = failure
	}
	return nil
}

// Seedはアイテムを複合キーで格納する。
// skを持たない旧形式のfixtureは、新しい複合キーへ正規化する。
func (f *DynaStore) Seed(item map[string]types.AttributeValue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	normalizeLegacyKey(item)
	f.items[keyID(item)] = item
}

// Itemは格納済みのアイテムを返す。
// skを省略した旧形式のキーは、新しい複合キーへ読み替える。
func (f *DynaStore) Item(pk string, sk ...string) map[string]types.AttributeValue {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := map[string]types.AttributeValue{"pk": &types.AttributeValueMemberS{Value: pk}}
	if len(sk) > 0 {
		key["sk"] = &types.AttributeValueMemberS{Value: sk[0]}
	}
	normalizeLegacyKey(key)
	return f.items[keyID(key)]
}

func (f *DynaStore) scan(_ context.Context, in *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["scan"]++
	if err := f.takeFailure("scan", ""); err != nil {
		return nil, err
	}
	var keys []string
	for key := range f.items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if in.ExclusiveStartKey != nil {
		after := keyID(in.ExclusiveStartKey)
		i := sort.SearchStrings(keys, after)
		if i < len(keys) && keys[i] == after {
			i++
		}
		keys = keys[i:]
	}
	out := &dynamodb.ScanOutput{}
	if n := f.scanPageSize; n > 0 && len(keys) > n {
		last := f.items[keys[n-1]]
		keys = keys[:n]
		out.LastEvaluatedKey = keyOf(last)
	}
	for _, key := range keys {
		out.Items = append(out.Items, f.items[key])
	}
	return out, nil
}

func (f *DynaStore) query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["query"]++
	if err := f.takeFailure("query", ""); err != nil {
		return nil, err
	}
	pk := in.ExpressionAttributeValues[":pk"].(*types.AttributeValueMemberS).Value
	var keys []string
	for key, item := range f.items {
		if pkOf(item) == pk {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if in.ExclusiveStartKey != nil {
		after := keyID(in.ExclusiveStartKey)
		i := sort.SearchStrings(keys, after)
		if i < len(keys) && keys[i] == after {
			i++
		}
		keys = keys[i:]
	}
	out := &dynamodb.QueryOutput{}
	if n := f.scanPageSize; n > 0 && len(keys) > n {
		last := f.items[keys[n-1]]
		keys = keys[:n]
		out.LastEvaluatedKey = keyOf(last)
	}
	for _, key := range keys {
		out.Items = append(out.Items, f.items[key])
	}
	return out, nil
}

func (f *DynaStore) batchGetItem(_ context.Context, in *dynamodb.BatchGetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.BatchGetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["batch-get"]++
	if err := f.takeFailure("batch-get", ""); err != nil {
		return nil, err
	}
	out := &dynamodb.BatchGetItemOutput{
		Responses:       map[string][]map[string]types.AttributeValue{},
		UnprocessedKeys: map[string]types.KeysAndAttributes{},
	}
	unprocessed := f.unprocessedBatchGetResponses > 0
	if unprocessed {
		f.unprocessedBatchGetResponses--
	}
	for table, request := range in.RequestItems {
		if unprocessed {
			out.UnprocessedKeys[table] = request
			continue
		}
		for _, key := range request.Keys {
			if item := f.items[keyID(key)]; item != nil {
				out.Responses[table] = append(out.Responses[table], item)
			}
		}
	}
	return out, nil
}

func (f *DynaStore) getItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["get"]++
	if err := f.takeFailure("get", failureKey(in.Key)); err != nil {
		return nil, err
	}
	return &dynamodb.GetItemOutput{Item: f.items[keyID(in.Key)]}, nil
}

func (f *DynaStore) putItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["put"]++
	normalizeLegacyKey(in.Item)
	if err := f.takeFailure("put", failureKey(in.Item)); err != nil {
		return nil, err
	}
	if !putConditionMatches(f.items[keyID(in.Item)], in) {
		return nil, &types.ConditionalCheckFailedException{}
	}
	f.items[keyID(in.Item)] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *DynaStore) updateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["update"]++
	if err := f.takeFailure("update", failureKey(in.Key)); err != nil {
		return nil, err
	}
	key := keyID(in.Key)
	item := f.items[key]
	if !updateConditionMatches(item, in) {
		return nil, &types.ConditionalCheckFailedException{}
	}
	if item == nil {
		item = keyOf(in.Key)
		f.items[key] = item
	}
	expr := *in.UpdateExpression
	setExpr, removeExpr := expr, ""
	if before, after, ok := strings.Cut(expr, " REMOVE "); ok {
		setExpr, removeExpr = before, after
	} else if after, ok := strings.CutPrefix(expr, "REMOVE "); ok {
		setExpr, removeExpr = "", after
	}
	setExpr = strings.TrimPrefix(setExpr, "SET ")
	for term := range strings.SplitSeq(setExpr, ", ") {
		if term == "" {
			continue
		}
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
	for name := range strings.SplitSeq(removeExpr, ", ") {
		if name == "" {
			continue
		}
		attr, ok := in.ExpressionAttributeNames[name]
		if !ok {
			return nil, fmt.Errorf("dynastore: unbound name %q", name)
		}
		delete(item, attr)
	}
	return &dynamodb.UpdateItemOutput{}, nil
}

func (f *DynaStore) deleteItem(_ context.Context, in *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["delete"]++
	if err := f.takeFailure("delete", failureKey(in.Key)); err != nil {
		return nil, err
	}
	key := keyID(in.Key)
	if !deleteConditionMatches(f.items[key], in) {
		return nil, &types.ConditionalCheckFailedException{}
	}
	delete(f.items, key)
	return &dynamodb.DeleteItemOutput{}, nil
}

func updateConditionMatches(item map[string]types.AttributeValue, in *dynamodb.UpdateItemInput) bool {
	if in.ConditionExpression == nil {
		return true
	}
	switch *in.ConditionExpression {
	case "attribute_exists(#pk)":
		return item != nil && item[in.ExpressionAttributeNames["#pk"]] != nil
	case "attribute_not_exists(#pk) OR #expires_at < :now":
		if item == nil || item[in.ExpressionAttributeNames["#pk"]] == nil {
			return true
		}
		actual, aok := numberValue(item[in.ExpressionAttributeNames["#expires_at"]])
		expected, eok := numberValue(in.ExpressionAttributeValues[":now"])
		return aok && eok && actual < expected
	case "attribute_not_exists(#pending_operation_id) OR #pending_operation_id = :empty":
		return attributeMissingOrEqual(item, in.ExpressionAttributeNames["#pending_operation_id"], in.ExpressionAttributeValues[":empty"])
	case "#pending_operation_id = :operation_id":
		return item != nil && equalAttributeValue(item[in.ExpressionAttributeNames["#pending_operation_id"]], in.ExpressionAttributeValues[":operation_id"])
	default:
		panic(fmt.Sprintf("dynastore: unsupported update condition %q", *in.ConditionExpression))
	}
}

func putConditionMatches(item map[string]types.AttributeValue, in *dynamodb.PutItemInput) bool {
	if in.ConditionExpression == nil {
		return true
	}
	if *in.ConditionExpression != "attribute_not_exists(#pk)" {
		panic(fmt.Sprintf("dynastore: unsupported put condition %q", *in.ConditionExpression))
	}
	return item == nil || item[in.ExpressionAttributeNames["#pk"]] == nil
}

func attributeMissingOrEqual(item map[string]types.AttributeValue, name string, expected types.AttributeValue) bool {
	return item == nil || item[name] == nil || equalAttributeValue(item[name], expected)
}

func deleteConditionMatches(item map[string]types.AttributeValue, in *dynamodb.DeleteItemInput) bool {
	if in.ConditionExpression == nil {
		return true
	}
	if *in.ConditionExpression != "#owner = :owner" {
		panic(fmt.Sprintf("dynastore: unsupported delete condition %q", *in.ConditionExpression))
	}
	return item != nil && equalAttributeValue(item[in.ExpressionAttributeNames["#owner"]], in.ExpressionAttributeValues[":owner"])
}

func equalAttributeValue(a, b types.AttributeValue) bool {
	switch av := a.(type) {
	case *types.AttributeValueMemberS:
		bv, ok := b.(*types.AttributeValueMemberS)
		return ok && av.Value == bv.Value
	case *types.AttributeValueMemberN:
		bv, ok := b.(*types.AttributeValueMemberN)
		return ok && av.Value == bv.Value
	default:
		return false
	}
}

func numberValue(v types.AttributeValue) (int64, bool) {
	n, ok := v.(*types.AttributeValueMemberN)
	if !ok {
		return 0, false
	}
	parsed, err := strconv.ParseInt(n.Value, 10, 64)
	return parsed, err == nil
}

func pkOf(item map[string]types.AttributeValue) string {
	if s, ok := item["pk"].(*types.AttributeValueMemberS); ok {
		return s.Value
	}
	return ""
}

func skOf(item map[string]types.AttributeValue) string {
	if s, ok := item["sk"].(*types.AttributeValueMemberS); ok {
		return s.Value
	}
	return ""
}

func keyID(item map[string]types.AttributeValue) string { return pkOf(item) + "\x00" + skOf(item) }

func keyOf(item map[string]types.AttributeValue) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"pk": item["pk"], "sk": item["sk"]}
}

func failureKey(item map[string]types.AttributeValue) string {
	pk, sk := pkOf(item), skOf(item)
	switch {
	case pk == "CONFIG" && strings.HasPrefix(sk, "GROUP#"):
		return "group#" + strings.TrimPrefix(sk, "GROUP#")
	case pk == "CONFIG" && strings.HasPrefix(sk, "OVERRIDE#"):
		return "override#" + strings.TrimPrefix(sk, "OVERRIDE#")
	case strings.HasPrefix(pk, "STATUS#"):
		return "status#" + strings.TrimPrefix(pk, "STATUS#")
	default:
		return pk
	}
}

func normalizeLegacyKey(item map[string]types.AttributeValue) {
	if skOf(item) != "" {
		return
	}
	pk := pkOf(item)
	switch {
	case strings.HasPrefix(pk, "group#"):
		item["pk"] = &types.AttributeValueMemberS{Value: "CONFIG"}
		item["sk"] = &types.AttributeValueMemberS{Value: "GROUP#" + strings.TrimPrefix(pk, "group#")}
	case strings.HasPrefix(pk, "override#"):
		item["pk"] = &types.AttributeValueMemberS{Value: "CONFIG"}
		item["sk"] = &types.AttributeValueMemberS{Value: "OVERRIDE#" + strings.TrimPrefix(pk, "override#")}
	case strings.HasPrefix(pk, "status#"):
		item["pk"] = &types.AttributeValueMemberS{Value: "STATUS#" + strings.TrimPrefix(pk, "status#")}
		item["sk"] = &types.AttributeValueMemberS{Value: "CURRENT"}
	}
}
