package model

import (
	"fmt"
)

// cheapskate がリソースをどの状態にしたいか
type DesiredState string

const (
	DesiredRunning DesiredState = "running"
	DesiredStopped DesiredState = "stopped"
	// 解決の結果として「望む状態がない」ことを表すゼロ値であり、disabled のグループがこれになる
	// 「空文字」と「未決定」を同じ表現にしないために名前を与えている
	DesiredNone DesiredState = ""
)

// d が running か stopped かを検査する
func (d DesiredState) Validate() error {
	if d != DesiredRunning && d != DesiredStopped {
		return fmt.Errorf("desired state must be running or stopped, got %q", d)
	}
	return nil
}

// 利用者が入力した文字列（CLI の位置引数、フォームの値）を DesiredState として解釈する
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

// Describe API 越しに見えた、ターゲットの実際の状態
// State はどの種別でも同じ語彙で、種別ごとに違う細部は Detail の自由文が運ぶ
// （ECS サービスなら "desiredCount=2" など）
// 種別固有のフィールドをここに足さないことで、この型を扱う reconcile や各 UI は種別を知らずに済む
type Observation struct {
	State  ObservedState `json:"state"`
	Detail string        `json:"detail,omitempty"`
}

// desired と observed の食い違いを解消するために reconciler が取る操作
type Action string

const (
	ActionStart Action = "start"
	ActionStop  Action = "stop"
	// 収束済みで何もしないことを表すゼロ値
	ActionNone Action = ""
)

// desired と observed の食い違いを解消するために取るべき操作を返す
// 収束済み、あるいは observed が遷移中・not-found なら ActionNone を返す
//
// 引数の型が違うので、desired 側に ObservedState の定数を渡すような取り違えはコンパイルが通らない
// 両者は "running"/"stopped" という同じ文字列を共有しているため、素の string ならこれは常に false になる比較として黙って通ってしまう
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
