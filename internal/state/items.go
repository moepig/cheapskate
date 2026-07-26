package state

import (
	"cheapskate/internal/core/model"
)

// テーブルのキー構成であり、pk はすべて "<kind>#<identity>" の形をとる
// これらの接頭辞が現れるのはここだけで、アプリケーション層はグループやリソースを名前で要求し、自分でキーを組み立てることはない
//
// groupKeyPrefix と model.GroupNamespace は理由の異なる同一文字列であり、あえて同じ定数にしていない
// こちらはグループの設定アイテム（"group#dev"）に名前空間を与える
// model 側は status キーの中でグループの合成リソース ID（"status#group#dev"）に名前空間を与える
// 一方を変えたとき、もう一方が黙って変わってはならない
const (
	groupKeyPrefix    = "group#"
	overrideKeyPrefix = "override#"
	statusKeyPrefix   = "status#"
)

func groupKey(name string) string            { return groupKeyPrefix + name }
func overrideKey(name string) string         { return overrideKeyPrefix + name }
func statusKey(resourceID string) string     { return statusKeyPrefix + resourceID }
func groupStatusKey(groupName string) string { return statusKey(model.GroupStatusID(groupName)) }

// 利用者が管理する `group#` アイテムの保存形状
// 各フィールドを model の名前付き型ではなく素の string にしているのは、これが保存形状であり
// ドメインの語彙との突き合わせを spec / newGroupItem の 2 か所に閉じるためである
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
// グループ名は i.PK から解析し直さず、引数で受け取る
// どの読み取り経路もすでに名前を知っている（キーを組み立てたか、scan 中に接頭辞と照合した）ためである
// よって壊れたキーを扱う場合分けは残っていない
// その検査が必要だったのは、ドメインの型が生のキーを持っていた頃の話である
//
// 未知の mode や desired はここでは弾かない
// 保存されたままの姿を返すのが GroupSpec の役目であり、妥当性を決めるのは model.ParseGroup である
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
// expires_at はテーブルの TTL 属性も兼ねるので、失効した override は DynamoDB が自ら回収する
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
// nil のフィールドはその属性に触れず、非 nil のフィールドはその値へ上書きする
// クリアしたいときは空文字を指す（Set("") を参照）
//
// ポインタにしているのは「触らない」と「空にする」を型で区別するためである
// 属性名とドメインの型の対応もここに閉じているので、アプリケーション層が DynamoDB の属性名を生文字列で書く経路はもう存在しない
// 属性名の打ち間違いが黙って別の属性を生やす、という失敗の仕方がなくなる
type StatusPatch struct {
	ObservedState      *model.ObservedState
	LastAction         *model.Action
	LastActionAt       *string
	LastError          *string
	LastErrorAt        *string
	TransitioningSince *string
}

// StatusPatch のフィールドへ渡すポインタを作る
// 「その属性を空にする」は Set("") であり、フィールドを nil のままにするのは「触らない」である
func Set[T ~string](v T) *T { return &v }

// 保存属性名と値の組
type statusAttr struct {
	name  string
	value string
}

// パッチが設定しているフィールドを、保存属性名と値の組へ並べる
// 順序は決定的であり、statusItem の dynamodbav タグと 1 対 1 で対応する
// status アイテムの属性はすべて文字列なので、値の型は 1 つで足りる
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
