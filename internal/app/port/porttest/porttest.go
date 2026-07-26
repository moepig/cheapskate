// port インターフェース向けの手書きテストダブルを置く
// アプリケーション層を駆動するすべてのパッケージ（internal/app/{reconcile,groups}、internal/ui/*）で共有する
//
// これらを mockgen のモックにしないのは意図的である
// ポートは 4 インターフェース・7 メソッドと小さく、引数もすべて internal/core/model の型である
// 利用側のテストが求めるのは呼び出しごとの期待値ではなく、状態を持つ振る舞い（用意した観測値、記録した stop/start 呼び出し）である
// 生成したモックを使っても、結局ここにあるようなダブルで包み直すことになる
// ひとつ外側の AWS SDK 境界（internal/aws/*、internal/state）はインターフェースが広く引数の型も重いので、生成する価値がある
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
// Resources と Err はあらゆるセレクタに対して答える
// ByTagValue と ErrByTagValue は、セレクタのタグ値をキーにしてグループ単位でそれらを上書きする
// フィクスチャのグループはタグ値を自身のグループ名にしているので、ByTagValue["dev"] = ... は「グループ dev の中身」と読める
// Selectors は渡されたセレクタを呼び出し順にすべて記録し、どのグループが探索されたか（disabled が除かれたか）の検証に使う
type Discoverer struct {
	Resources     []model.Resource
	Err           error
	ByTagValue    map[string][]model.Resource
	ErrByTagValue map[string]error
	Selectors     []model.Selector
}

// グループ単位のマップを書き込める状態にした Discoverer を返す
// Resources と Err しか要らないテストならゼロ値のままでよい
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
// Describe は Observations から答え、未登録なら StateNotFound を返す（探索後に消えたリソースの見え方にあたる）
// Stop/Start は対応するエラーフィールドが未設定なら、呼ばれた ref を記録する
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

// どの ref に対しても固定の Observation（またはエラー）を返す port.Describer のテストダブル
// 値型なので map[model.ResourceType]port.Describer のリテラルへそのまま入れられる
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
