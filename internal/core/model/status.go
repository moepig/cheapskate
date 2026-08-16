package model

import (
	"strings"
)

// リソース 1 件について reconciler が所有する監査証跡
// 収束済みのサイクルでは書き込みを行わない。定常状態におけるリソースごとの書き込みを避けるためである
// したがって各フィールドは last_action_at / last_error_at 時点のスナップショットであり、現在の状態ではない
// 最後のアクション以降に cheapskate の外部でリソースが変更された場合、ObservedState は現在の状態と一致しない
// グループ単位の設定エラーと探索エラーも、合成リソース ID である GroupStatusID(name) の下に同じ形状で記録する
// TransitioningSince のみは性質が異なり、スナップショットではなく継続中の遷移の開始時刻を表す
// starting/stopping/modifying を最初に観測したサイクルで 1 回だけ書き、遷移でない状態を観測した時点で削除する
// これがない場合、完了しない停止処理とドレインを、収束済みのリソースと区別できない
// 遷移中のリソースは skip の対象であり、エラーにも通知にも現れないためである
// 遷移が停滞したとみなす経過時間は reconciler では判定せず、doctor と各 UI が経過時間として提示する
type Status struct {
	ObservedState      ObservedState `json:"observed_state,omitempty"`
	LastAction         Action        `json:"last_action,omitempty"`
	LastActionAt       string        `json:"last_action_at,omitempty"`
	LastError          string        `json:"last_error,omitempty"`
	LastErrorAt        string        `json:"last_error_at,omitempty"`
	TransitioningSince string        `json:"transitioning_since,omitempty"`
}

// グループ自身のステータスを記録する合成リソース ID の接頭辞
// 実リソースの Resource.ID() と衝突することはない
// リソース種別の定数が "group" とならないためである (KnownTypes を参照)
const GroupNamespace = "group#"

// グループ単位の設定と探索の失敗を記録する先となる、合成リソース ID を返す
// これによりグループ単位と個別リソースのステータスは、同じ形状と同じ通知重複排除の経路を共有する
func GroupStatusID(name string) string { return GroupNamespace + name }

// GroupStatusID の逆変換
// 実リソースの ID に対しては false を返す
func GroupFromStatusID(id string) (string, bool) { return strings.CutPrefix(id, GroupNamespace) }
