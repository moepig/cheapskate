// cheapskate の永続化レイヤであり、DynamoDB の state テーブルを扱う
// テーブルのキー構成とアイテム形状を知っているのはここだけである（items.go を参照）
// reconciler にとって group# アイテムは読み取り専用で、status# アイテムは reconciler が所有する
// CLI はさらに group# と override# のアイテムも書き込む
//
// port の背後にある internal/aws のアダプタにしていないのは意図的である
// state テーブルは差し替え可能な依存ではなく cheapskate に固有のものなので、アプリケーション層はこのパッケージへ直接依存する
// テストではその下にある DynamoDB クライアントの側をモックする
package state

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

	"cheapskate/internal/core/model"
)

//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks cheapskate/internal/state API

// store が使う DynamoDB クライアントの部分集合
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

// ターゲットグループ 1 件の設定に、1 回の ScanAll で得た override とグループ単位のステータスを結合したもの
// ここでの Status は status#group#<name> アイテム（設定や探索の失敗）であり、所属リソースのステータスではない
// リソース側のステータスは resource_id をキーとして ScanResult.Statuses に入る
// store だけでは、どのリソースが今このグループに属するか判断できないためである（グループのセレクタに対する動的な Discover が必要になる）
type GroupRow struct {
	Name     string
	Group    model.GroupSpec
	HasGroup bool // override や group-status はあるのに group# アイテムがない場合は false（孤立データ）
	Override *model.Override
	Status   model.Status
	Err      error // このグループの override、group-status、group# のいずれかが unmarshal や検証に失敗したときに設定される
}

// ScanAll の結合結果で、全グループ行と、見つかったリソース単位の status# アイテムすべてを含む
// 後者はグループ単位のものを除き、resource_id をキーとした平坦なマップになる
// どのステータスがどのグループのものかを知るには、呼び出し側が Statuses と動的な Discover() の結果を突き合わせる
type ScanResult struct {
	Groups   []GroupRow
	Statuses map[string]model.Status
}

// テーブル全体をページングしながら 1 回 Scan し、group・override・group-status のアイテムをグループ名で結合する
// これにより group を引いてから GetOverride、GetStatus と続く N+1 の GetItem を、Scan 1 回に置き換える
//
// 壊れた override や group-status のアイテムは scan を中断せず、そのグループ行の Err として記録する
// 悪い行 1 つで他のすべての一覧を落としてはならないためである
// 壊れたリソース単位の status アイテムは単に飛ばす
// エラーを紐づけるグループ行を持たない孤立した監査記録であり、リソースとグループの対応づけにはこの scan ではなく動的な探索が必要になる
func (s *Store) ScanAll(ctx context.Context, now time.Time) (ScanResult, error) {
	var raws []map[string]types.AttributeValue
	var startKey map[string]types.AttributeValue
	for {
		out, err := s.db.Scan(ctx, &dynamodb.ScanInput{TableName: &s.table, ExclusiveStartKey: startKey})
		if err != nil {
			return ScanResult{}, fmt.Errorf("scan all: %w", err)
		}
		raws = append(raws, out.Items...)
		if out.LastEvaluatedKey == nil {
			break
		}
		startKey = out.LastEvaluatedKey
	}

	rows := map[string]*GroupRow{}
	var order []string
	rowFor := func(name string) *GroupRow {
		r, ok := rows[name]
		if !ok {
			r = &GroupRow{Name: name}
			rows[name] = r
			order = append(order, name)
		}
		return r
	}

	statuses := map[string]model.Status{}

	for _, raw := range raws {
		pkAttr, ok := raw["pk"].(*types.AttributeValueMemberS)
		if !ok {
			continue
		}
		pk := pkAttr.Value
		switch {
		case strings.HasPrefix(pk, groupKeyPrefix):
			name := strings.TrimPrefix(pk, groupKeyPrefix)
			var item groupItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				rowFor(name).Err = fmt.Errorf("unmarshal group %s: %w", name, err)
				continue
			}
			r := rowFor(name)
			r.Group, r.HasGroup = item.spec(name), true
		case strings.HasPrefix(pk, overrideKeyPrefix):
			name := strings.TrimPrefix(pk, overrideKeyPrefix)
			var item overrideItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				rowFor(name).Err = fmt.Errorf("unmarshal override %s: %w", name, err)
				continue
			}
			o := item.override()
			if o.ExpiresAt <= now.Unix() {
				continue // 失効済みであり、DynamoDB の TTL 削除は遅延するので GetOverride と同様ここで期限を強制する
			}
			if o.Desired.Validate() != nil {
				rowFor(name).Err = fmt.Errorf("%s: override desired must be running|stopped", name)
				continue
			}
			rowFor(name).Override = &o
		case strings.HasPrefix(pk, statusKeyPrefix):
			resourceID := strings.TrimPrefix(pk, statusKeyPrefix)
			var item statusItem
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				if name, ok := model.GroupFromStatusID(resourceID); ok {
					rowFor(name).Err = fmt.Errorf("unmarshal group status %s: %w", name, err)
				}
				continue
			}
			if name, ok := model.GroupFromStatusID(resourceID); ok {
				rowFor(name).Status = item.status()
			} else {
				statuses[resourceID] = item.status()
			}
		}
	}

	sort.Strings(order)
	result := ScanResult{Statuses: statuses, Groups: make([]GroupRow, 0, len(order))}
	for _, name := range order {
		result.Groups = append(result.Groups, *rows[name])
	}
	return result, nil
}

// 保存されたグループの spec を返す
// 未登録なら nil を返す
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

// グループの spec を書き込む（CLI と web console 専用で、reconciler がこれを呼ぶことはない）
func (s *Store) PutGroup(ctx context.Context, spec model.GroupSpec) error {
	av, err := attributevalue.MarshalMap(newGroupItem(spec))
	if err != nil {
		return fmt.Errorf("marshal group: %w", err)
	}
	_, err = s.db.PutItem(ctx, &dynamodb.PutItemInput{TableName: &s.table, Item: av})
	return err
}

// グループの未失効な override を返し、なければ nil を返す
// DynamoDB の TTL 削除は遅延するため、期限はここで強制する
func (s *Store) GetOverride(ctx context.Context, group string, now time.Time) (*model.Override, error) {
	raw, err := s.get(ctx, overrideKey(group))
	if err != nil || raw == nil {
		return nil, err
	}
	var item overrideItem
	if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
		return nil, fmt.Errorf("unmarshal override %s: %w", group, err)
	}
	o := item.override()
	if o.ExpiresAt <= now.Unix() {
		return nil, nil
	}
	if o.Desired.Validate() != nil {
		return nil, fmt.Errorf("%s: override desired must be running|stopped", group)
	}
	return &o, nil
}

// TTL 属性付きの override アイテムを書き込む（CLI 専用）
func (s *Store) PutOverride(ctx context.Context, group string, o model.Override) error {
	_, err := s.db.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &s.table,
		Item: map[string]types.AttributeValue{
			"pk":         &types.AttributeValueMemberS{Value: overrideKey(group)},
			"desired":    &types.AttributeValueMemberS{Value: string(o.Desired)},
			"expires_at": &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", o.ExpiresAt)},
		},
	})
	return err
}

// status アイテムを返し、存在しなければゼロ値の Status を返す
// resourceID はリソース ID（"ecs-service#dev-cluster/api" など）でも、グループ単位の擬似 ID（"group#<name>"）でもよい
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

// p が設定している属性だけを status アイテムへ SET する
// 設定が 1 つもなければ何も呼ばない
// どの属性名を書くかは StatusPatch 側にある（items.go を参照）ので、この関数は属性名を知らない
func (s *Store) UpdateStatus(ctx context.Context, resourceID string, p StatusPatch) error {
	attrs := p.attributes()
	if len(attrs) == 0 {
		return nil
	}
	names := make(map[string]string, len(attrs))
	values := make(map[string]types.AttributeValue, len(attrs))
	terms := make([]string, 0, len(attrs))
	for i, a := range attrs {
		n, v := fmt.Sprintf("#a%d", i), fmt.Sprintf(":v%d", i)
		names[n] = a.name
		values[v] = &types.AttributeValueMemberS{Value: a.value}
		terms = append(terms, n+" = "+v)
	}
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 &s.table,
		Key:                       key(statusKey(resourceID)),
		UpdateExpression:          aws.String("SET " + strings.Join(terms, ", ")),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: values,
	})
	return err
}

// グループの設定アイテムを削除する（CLI と web console 専用）
func (s *Store) DeleteGroup(ctx context.Context, name string) error {
	return s.delete(ctx, groupKey(name))
}

// TTL を待たずにグループの override を今すぐ削除する
func (s *Store) DeleteOverride(ctx context.Context, name string) error {
	return s.delete(ctx, overrideKey(name))
}

// グループ単位のステータスレコードを削除する
func (s *Store) DeleteGroupStatus(ctx context.Context, name string) error {
	return s.delete(ctx, groupStatusKey(name))
}

// リソース単位のステータスレコードを削除する（doctor --prune 専用）
// reconciler がこれを呼ぶことはない
// 探索の結果は Tagging API の遅れで数分ずれるため、reconcile の最中に「今マッチしていない」ことを根拠に監査記録を消すのは、一時的に見えていないだけのリソースの履歴を落とすことになる
func (s *Store) DeleteStatus(ctx context.Context, resourceID string) error {
	return s.delete(ctx, statusKey(resourceID))
}

// アイテムの生の pk を返す（doctor が手作業の delete-item 用に提示するため）
// キー構成を知るのはこのパッケージだけ、という原則を崩さずに pk を外へ出す唯一の経路である
func StatusPK(resourceID string) string { return statusKey(resourceID) }
func GroupPK(name string) string        { return groupKey(name) }
func OverridePK(name string) string     { return overrideKey(name) }
func GroupStatusPK(name string) string  { return groupStatusKey(name) }

// アイテム 1 件を削除する
// 存在しないアイテムの削除はエラーにならない
func (s *Store) delete(ctx context.Context, pk string) error {
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
