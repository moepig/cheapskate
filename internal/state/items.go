package state

import (
	"cheapskate/internal/core/model"
)

// テーブルのキー構成であり、pk はすべて "<kind>#<identity>" の形式をとる
// これらの接頭辞が現れるのは本ファイルに限る。アプリケーション層はグループとリソースを名前で指定し、キーを組み立てない
//
// groupKeyPrefix と model.GroupNamespace は、根拠の異なる同一の文字列であり、同じ定数としない
// 本定数はグループの設定アイテムへ名前空間を与える
// model 側は status キーにおいて、グループの合成リソース ID へ名前空間を与える
// 一方の変更が他方へ波及してはならないためである
const (
	groupKeyPrefix    = "group#"
	overrideKeyPrefix = "override#"
	statusKeyPrefix   = "status#"
)

func groupKey(name string) string            { return groupKeyPrefix + name }
func overrideKey(name string) string         { return overrideKeyPrefix + name }
func statusKey(resourceID string) string     { return statusKeyPrefix + resourceID }
func groupStatusKey(groupName string) string { return statusKey(model.GroupStatusID(groupName)) }

// 設定操作が管理する `group#` アイテムの保存形状
// 各フィールドを model の名前付き型ではなく string とするのは、これが保存形状であり、
// ドメインの語彙との対応づけを spec と newGroupItem の 2 か所へ限定するためである
type groupItem struct {
	PK        string   `dynamodbav:"pk"`
	Mode      string   `dynamodbav:"mode,omitempty"`
	Desired   string   `dynamodbav:"desired,omitempty"`
	StartCron string   `dynamodbav:"start_cron,omitempty"`
	StopCron  string   `dynamodbav:"stop_cron,omitempty"`
	Timezone  string   `dynamodbav:"timezone,omitempty"`
	TagKey    string   `dynamodbav:"tag_key,omitempty"`
	TagValue  string   `dynamodbav:"tag_value,omitempty"`
	Types     []string `dynamodbav:"types,stringset,omitempty"`
}

func newGroupItem(spec model.GroupSpec) groupItem {
	return groupItem{
		PK:        groupKey(spec.Name),
		Mode:      string(spec.Mode),
		Desired:   string(spec.Desired),
		StartCron: spec.StartCron,
		StopCron:  spec.StopCron,
		Timezone:  spec.Timezone,
		TagKey:    spec.TagKey,
		TagValue:  spec.TagValue,
		Types:     model.TypeNames(spec.Types),
	}
}

// アイテムをドメインの型へ復号する
// グループ名は i.PK から解析せず、引数で受け取る
// いずれの読み取り経路も、キーの組み立てまたは scan 中の接頭辞の照合により、すでに名前を保持するためである
// したがって、壊れたキーに対する分岐を持たない
//
// 未知の mode と desired は、ここでは拒否しない
// 保存された内容をそのまま返すことが GroupSpec の役割であり、妥当性の判定は model.ParseGroup が行う
func (i groupItem) spec(name string) model.GroupSpec {
	return model.GroupSpec{
		Name:      name,
		Mode:      model.Mode(i.Mode),
		Desired:   model.DesiredState(i.Desired),
		StartCron: i.StartCron,
		StopCron:  i.StopCron,
		Timezone:  i.Timezone,
		TagKey:    i.TagKey,
		TagValue:  i.TagValue,
		Types:     model.ResourceTypes(i.Types),
	}
}

// `override#` アイテムの保存形状
// expires_at はテーブルの TTL 属性を兼ねるため、失効した override は DynamoDB が削除する
type overrideItem struct {
	Desired   string `dynamodbav:"desired"`
	ExpiresAt int64  `dynamodbav:"expires_at"`
}

func (i overrideItem) override() model.Override {
	return model.Override{Desired: model.DesiredState(i.Desired), ExpiresAt: i.ExpiresAt}
}

// reconciler が所有する `status#` アイテムの保存形状
type statusItem struct {
	ObservedState      string `dynamodbav:"observed_state,omitempty"`
	LastAction         string `dynamodbav:"last_action,omitempty"`
	LastActionAt       string `dynamodbav:"last_action_at,omitempty"`
	LastError          string `dynamodbav:"last_error,omitempty"`
	LastErrorAt        string `dynamodbav:"last_error_at,omitempty"`
	TransitioningSince string `dynamodbav:"transitioning_since,omitempty"`
}

func (i statusItem) status() model.Status {
	return model.Status{
		ObservedState:      model.ObservedState(i.ObservedState),
		LastAction:         model.Action(i.LastAction),
		LastActionAt:       i.LastActionAt,
		LastError:          i.LastError,
		LastErrorAt:        i.LastErrorAt,
		TransitioningSince: i.TransitioningSince,
	}
}

// status アイテムの部分更新
// nil のフィールドは対応する属性を変更せず、非 nil のフィールドはその値へ上書きする
// 属性を空にする場合は空文字を指す (Set("") を参照)
//
// ポインタとするのは、変更しないことと空にすることを型で区別するためである
// 属性名とドメインの型の対応も本ファイルへ限定するため、アプリケーション層が DynamoDB の属性名を文字列で記述する経路は存在しない
// これにより、属性名の誤記が別の属性の生成として現れることはない
type StatusPatch struct {
	ObservedState      *model.ObservedState
	LastAction         *model.Action
	LastActionAt       *string
	LastError          *string
	LastErrorAt        *string
	TransitioningSince *string
}

// StatusPatch のフィールドへ渡すポインタを返す
// 属性を空にする場合は Set("") とし、変更しない場合はフィールドを nil のままとする
func Set[T ~string](v T) *T { return &v }

// 保存属性名と値の組
type statusAttr struct {
	name  string
	value string
}

// パッチが設定しているフィールドを、保存属性名と値の組として返す
// 順序は決定的であり、statusItem の dynamodbav タグと 1 対 1 で対応する
// status アイテムの属性はすべて文字列であるため、値の型は 1 つで足りる
func (p StatusPatch) attributes() []statusAttr {
	var out []statusAttr
	if p.ObservedState != nil {
		out = append(out, statusAttr{"observed_state", string(*p.ObservedState)})
	}
	if p.LastAction != nil {
		out = append(out, statusAttr{"last_action", string(*p.LastAction)})
	}
	if p.LastActionAt != nil {
		out = append(out, statusAttr{"last_action_at", *p.LastActionAt})
	}
	if p.LastError != nil {
		out = append(out, statusAttr{"last_error", *p.LastError})
	}
	if p.LastErrorAt != nil {
		out = append(out, statusAttr{"last_error_at", *p.LastErrorAt})
	}
	if p.TransitioningSince != nil {
		out = append(out, statusAttr{"transitioning_since", *p.TransitioningSince})
	}
	return out
}
