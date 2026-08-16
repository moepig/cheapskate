// port インターフェース向けの手書きテストダブルを置く
// アプリケーション層を駆動するすべてのパッケージ (internal/app/{reconcile,groups}、internal/ui/*) で共有する
//
// これらは mockgen による生成の対象としない
// ポートは 4 インターフェース、7 メソッドであり、引数はすべて internal/core/model の型である
// 利用側のテストが必要とするのは、呼び出しごとの期待値ではなく、状態を持つ振る舞いである
// 生成したモックを用いる場合も、ここにあるダブルと同等の実装で包む必要がある
// 1 つ外側の AWS SDK 境界 (internal/aws/*、internal/state) はインターフェースが広く引数の型も大きいため、生成の対象とする
package porttest

import (
	"context"

	"cheapskate/internal/app/port"
	"cheapskate/internal/core/model"
)

var (
	_ port.Discoverer = (*Discoverer)(nil)
	_ port.Target     = (*Target)(nil)
	_ port.Describer  = Describer{}
	_ port.Notifier   = (*Notifier)(nil)
)

// port.Discoverer のテストダブル
// Resources と Err は、すべてのセレクタに対する応答となる
// ByTagValue と ErrByTagValue は、セレクタのタグ値をキーとしてグループ単位でそれらを上書きする
// フィクスチャのグループはタグ値をグループ名と一致させるため、ByTagValue のキーはグループ名に対応する
// Selectors は渡されたセレクタを呼び出し順に記録し、探索されたグループの検証に用いる
type Discoverer struct {
	Resources     []model.Resource
	Err           error
	ByTagValue    map[string][]model.Resource
	ErrByTagValue map[string]error
	Selectors     []model.Selector
}

// グループ単位のマップを初期化した Discoverer を返す
// Resources と Err のみを用いるテストでは、ゼロ値で足りる
func NewDiscoverer() *Discoverer {
	return &Discoverer{ByTagValue: map[string][]model.Resource{}, ErrByTagValue: map[string]error{}}
}

func (d *Discoverer) Discover(_ context.Context, sel model.Selector) ([]model.Resource, error) {
	d.Selectors = append(d.Selectors, sel)
	if err, ok := d.ErrByTagValue[sel.TagValue]; ok {
		return nil, err
	}
	if d.Err != nil {
		return nil, d.Err
	}
	if res, ok := d.ByTagValue[sel.TagValue]; ok {
		return res, nil
	}
	return d.Resources, nil
}

// これまでの Discover の呼び出し回数を返す
func (d *Discoverer) Calls() int { return len(d.Selectors) }

// 状態を持つ port.Target のテストダブル
// Describe は Observations を参照し、未登録の場合は StateNotFound を返す
// Stop/Start は、対応するエラーフィールドが未設定の場合に、呼ばれた ref を記録する
type Target struct {
	Typ          model.ResourceType
	Observations map[string]model.Observation
	DescribeErr  error
	StopErr      error
	StartErr     error
	Stopped      []string
	Started      []string
}

func NewTarget(typ model.ResourceType) *Target {
	return &Target{Typ: typ, Observations: map[string]model.Observation{}}
}

func (t *Target) Type() model.ResourceType { return t.Typ }

func (t *Target) Describe(_ context.Context, ref string) (model.Observation, error) {
	if t.DescribeErr != nil {
		return model.Observation{}, t.DescribeErr
	}
	if obs, ok := t.Observations[ref]; ok {
		return obs, nil
	}
	return model.Observation{State: model.StateNotFound}, nil
}

func (t *Target) Stop(_ context.Context, ref string) error {
	if t.StopErr != nil {
		return t.StopErr
	}
	t.Stopped = append(t.Stopped, ref)
	return nil
}

func (t *Target) Start(_ context.Context, res model.Resource) error {
	if t.StartErr != nil {
		return t.StartErr
	}
	t.Started = append(t.Started, res.Ref)
	return nil
}

// すべての ref に対して固定の Observation またはエラーを返す port.Describer のテストダブル
// 値型であるため、map[model.ResourceType]port.Describer のリテラルへ直接記述できる
type Describer struct {
	Obs model.Observation
	Err error
}

func (d Describer) Describe(context.Context, string) (model.Observation, error) {
	return d.Obs, d.Err
}

// 記録された Notifier.Publish の呼び出し 1 件
type Notification struct {
	Subject string
	Payload map[string]any
}

// publish をすべて記録する port.Notifier のテストダブル
// Err を設定すると各呼び出しが失敗する
type Notifier struct {
	Published []Notification
	Err       error
}

func (n *Notifier) Publish(_ context.Context, subject string, payload map[string]any) error {
	n.Published = append(n.Published, Notification{Subject: subject, Payload: payload})
	return n.Err
}
