package model

import (
	"fmt"
)

// cheapskate がリソースをどの状態にしたいか
type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
	// 解決の結果として desired state が存在しないことを表すゼロ値であり、disabled のグループが該当する
	// 空文字と未決定を同じ表現としないため、名前を与える
	DesiredNone DesiredState = ""
)

// d が running か stopped かを検査する
func (d DesiredState) Validate() error {
	if d != DesiredRunning && d != DesiredStopped {
		return fmt.Errorf("desired state must be running or stopped, got %q", d)
	}
	return nil
}

// 入力された文字列を DesiredState として解釈する
func ParseDesired(s string) (DesiredState, error) {
	d := DesiredState(s)
	return d, d.Validate()
}

// Describe API 越しに見えたリソースの実際の状態
type ObservedState string

const (
	StateRunning       ObservedState = "running"
	StateStopped       ObservedState = "stopped"
	StateTransitioning ObservedState = "transitioning"
	StateNotFound      ObservedState = "not-found"
)

// Describe API を通じて観測したターゲットの状態
// State の語彙は全種別で共通であり、種別ごとに異なる詳細は Detail の自由記述が保持する
// 種別固有のフィールドを追加しないことにより、この型を扱う reconcile と各 UI は種別を参照しない
type Observation struct {
	State  ObservedState `json:"state"`
	Detail string        `json:"detail,omitempty"`
}

// desired と observed の食い違いを解消するために reconciler が取る操作
type Action string

const (
	ActionStart Action = "start"
	ActionStop  Action = "stop"
	// 収束済みであり操作を行わないことを表すゼロ値
	ActionNone Action = ""
)

// desired と observed の差異を解消するために取るべき操作を返す
// 収束済みの場合、および observed が遷移中または not-found の場合は ActionNone を返す
//
// 引数の型が異なるため、desired へ ObservedState の定数を渡した場合はコンパイルが通らない
// 両者は "running"/"stopped" という同じ文字列を共有するため、string とした場合、この誤りは常に false となる比較として通過する
func DecideAction(desired DesiredState, observed ObservedState) Action {
	switch {
	case desired == DesiredStopped && observed == StateRunning:
		return ActionStop
	case desired == DesiredRunning && observed == StateStopped:
		return ActionStart
	default:
		return ActionNone
	}
}
