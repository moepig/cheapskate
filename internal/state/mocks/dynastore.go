// 生成された MockAPI を裏で支える、手書きのインメモリ実装
// 実装しているのは store が実際に発行する式の形だけである（begins_with の scan フィルタ、SET の更新式）
// 加えて、store の再試行やページングの経路を通すためのエラー注入と Scan のページ送りを持つ
// 条件式や他の演算子を持たせず、あえてこの狭さに留めている
// store がそれ以上を発行しないためであり、完全な DynamoDB エミュレータへ育てるのではなくこの状態を保つこと
// これは旧 internal/dynafake パッケージをそのまま移したものである
// 手書きのインターフェース実装ではなく gomock を継ぎ目にするため、MockAPI に繋いである
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

// Scan・GetItem・PutItem・UpdateItem・DeleteItem をインメモリのテーブルで裏打ちした、生成済みの MockAPI を返す
// 併せて、初期データ投入・内容確認・失敗注入のための状態ハンドルも返す
func NewDynaStore(ctrl *gomock.Controller) (*MockAPI, *DynaStore) {
	st := &DynaStore{items: map[string]map[string]types.AttributeValue{}}
	m := NewMockAPI(ctrl)
	m.EXPECT().Scan(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.scan)
	m.EXPECT().GetItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.getItem)
	m.EXPECT().PutItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.putItem)
	m.EXPECT().UpdateItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.updateItem)
	m.EXPECT().DeleteItem(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(st.deleteItem)
	return m, st
}

// 指定した操作（"get"・"put"・"update"・"delete"・"scan"）の次に合致する呼び出しが err を返すようにする
// get/put/update/delete では pk がそのキーに限定し、"" を渡すとその操作の任意のキーに合致する
// 一度発火したら自ら解除されるので、後続の呼び出し（直後の再試行など）に影響を与えず単発の失敗を注入できる
func (f *DynaStore) FailOn(op, pk string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail == nil {
		f.fail = map[string]error{}
	}
	f.fail[op+"|"+pk] = err
}

// Scan が 1 回あたり最大 n 件だけ返すようにし、LastEvaluatedKey を報告して呼び出し側に残りのページ送りを強いる
// 既定値の 0 は無制限（1 ページ）を意味する
func (f *DynaStore) SetScanPageSize(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scanPageSize = n
}

// (op, pk) に注入された失敗を取り出して返す
// 操作全体に対する "" の指定よりも、キー個別の指定を優先する
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

// pk 属性をキーにしてアイテムを格納する
func (f *DynaStore) Seed(item map[string]types.AttributeValue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[pkOf(item)] = item
}

// 格納済みのアイテムを返す（存在しなければ nil）
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
	for term := range strings.SplitSeq(expr, ", ") {
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
