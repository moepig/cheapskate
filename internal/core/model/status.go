package model

import (
	"strings"
)

// リソース 1 件について reconciler が所有する監査証跡
// 収束済みのサイクルは何も書き込まない（定常状態のリソースごとに 5 分おきの書き込みが起きるのを避ける設計）
// そのため各フィールドは last_action_at / last_error_at 時点のスナップショットであり、現在のライブな状態ではない
// たとえば最後のアクション後に cheapskate の外部でリソースが変更されると、ObservedState は古くなる
// グループ単位の設定・探索エラーも、合成リソース ID である GroupStatusID(name) の下に同じ形状で記録される
// TransitioningSince だけは他と性質が違い、スナップショットではなく「今まさに継続している遷移の開始時刻」である
// starting/stopping/modifying を最初に観測したサイクルで 1 回だけ書き、遷移でない状態を観測した時点で消す
// これがないと、止まらない停止処理や終わらないドレインが、収束済みのリソースと完全に区別できない
// 遷移中のリソースは skip されるだけでエラーにも通知にもならないためである
// 何分で「詰まっている」とみなすかは reconciler 側では判断せず、doctor と各 UI が経過時間として提示する
type Status struct {
	ObservedState      ObservedState `json:"observed_state,omitempty"`
	LastAction         Action        `json:"last_action,omitempty"`
	LastActionAt       string        `json:"last_action_at,omitempty"`
	LastError          string        `json:"last_error,omitempty"`
	LastErrorAt        string        `json:"last_error_at,omitempty"`
	TransitioningSince string        `json:"transitioning_since,omitempty"`
}

// グループ自身のステータスを記録する合成リソース ID に付く接頭辞
// 実リソースの Resource.ID() と衝突することはない
// リソース種別の定数が決して "group" にならないためである（KnownTypes を参照）
const GroupNamespace = "group#"

// グループ単位の設定・探索の失敗を記録する先となる、合成リソース ID を返す
// これによりグループ単位と個別リソースのステータスが、同じ形状と同じ通知重複排除の経路を共有できる
func GroupStatusID(name string) string { return GroupNamespace + name }

// GroupStatusID の逆変換
// 実リソースの ID に対しては false を返す
func GroupFromStatusID(id string) (string, bool) { return strings.CutPrefix(id, GroupNamespace) }
