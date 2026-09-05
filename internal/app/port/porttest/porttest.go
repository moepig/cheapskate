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
// Resources は固定タグで検出するリソースを表し、Err は検出全体の失敗を表す
type Discoverer struct {
	Resources map[string]model.Resource
	Err       error
	CallsN    int
}

// 空のリソースマップを持つ Discoverer を返す
func NewDiscoverer() *Discoverer {
	return &Discoverer{Resources: map[string]model.Resource{}}
}

func (d *Discoverer) Discover(context.Context) (map[string]model.Resource, error) {
	d.CallsN++
	if d.Err != nil {
		return nil, d.Err
	}
	return d.Resources, nil
}

// これまでの Discover の呼び出し回数を返す
func (d *Discoverer) Calls() int { return d.CallsN }

// 状態を持つ port.Target のテストダブル
// Describe は Observations を参照し、未登録の場合は StateNotFound を返す
// 各操作は、リソース別または共通のエラーが未設定の場合に、呼ばれた ref を記録する
type Target struct {
	Typ          model.ResourceType
	Observations map[string]model.Observation
	DescribeErr  error
	DescribeErrs map[string]error
	StopErr      error
	StopErrs     map[string]error
	StartErr     error
	StartErrs    map[string]error
	Described    []string
	Stopped      []string
	Started      []string
}

func NewTarget(typ model.ResourceType) *Target {
	return &Target{
		Typ: typ, Observations: map[string]model.Observation{},
		DescribeErrs: map[string]error{}, StopErrs: map[string]error{}, StartErrs: map[string]error{},
	}
}

func (t *Target) Type() model.ResourceType { return t.Typ }

func (t *Target) Describe(_ context.Context, res model.Resource) (model.Observation, error) {
	t.Described = append(t.Described, res.Ref)
	if err := t.DescribeErrs[res.Ref]; err != nil {
		return model.Observation{}, err
	}
	if t.DescribeErr != nil {
		return model.Observation{}, t.DescribeErr
	}
	if obs, ok := t.Observations[res.Ref]; ok {
		return obs, nil
	}
	return model.Observation{State: model.StateNotFound}, nil
}

func (t *Target) Stop(_ context.Context, res model.Resource) error {
	if err := t.StopErrs[res.Ref]; err != nil {
		return err
	}
	if t.StopErr != nil {
		return t.StopErr
	}
	t.Stopped = append(t.Stopped, res.Ref)
	return nil
}

func (t *Target) Start(_ context.Context, res model.Resource) error {
	if err := t.StartErrs[res.Ref]; err != nil {
		return err
	}
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

func (d Describer) Describe(context.Context, model.Resource) (model.Observation, error) {
	return d.Obs, d.Err
}

// 記録された Notifier.Publish の呼び出し 1 件
type Notification struct {
	Subject string
	Payload map[string]any
}

// publish をすべて記録する port.Notifier のテストダブル
// Errors は呼び出しごとに先頭から返す。空になった後は Err を返す
type Notifier struct {
	Published []Notification
	Err       error
	Errors    []error
}

func (n *Notifier) Publish(_ context.Context, subject string, payload map[string]any) error {
	n.Published = append(n.Published, Notification{Subject: subject, Payload: payload})
	if len(n.Errors) > 0 {
		err := n.Errors[0]
		n.Errors = n.Errors[1:]
		return err
	}
	return n.Err
}
