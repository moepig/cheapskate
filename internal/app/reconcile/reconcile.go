// reconcile ループを持つ
// スケジュール起動でも RDS イベント起動でも、呼び出しごとに必ず全体を reconcile する
// RDS イベント時に単一リソースへ絞る reconcile は、グループ所属が静的なメンバー登録から AWS タグの動的探索へ移った時点で廃止した
// 絞り込んでも結局は全体 reconcile と同じコストになるためである
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cheapskate/internal/app/port"
	"cheapskate/internal/core/model"
	"cheapskate/internal/core/schedule"
	"cheapskate/internal/state"
)

// reconciler が state テーブルに求める範囲
// *state.Store が満たすが、reconciler が受け取るのはこの窓口だけである
//
// グループ設定と override を書くメソッドを意図的に含めていない
// それらを所有するのは CLI と web console であり、reconciler にとっては読み取り専用の入力である
// この不変条件はこれまでコメントでしか宣言されていなかった
// 型として書いておけば、reconcile の中から設定を書き換える経路がそもそも存在しなくなる
type Store interface {
	ScanAll(ctx context.Context, now time.Time) (state.ScanResult, error)
	GetStatus(ctx context.Context, resourceID string) (model.Status, error)
	UpdateStatus(ctx context.Context, resourceID string, p state.StatusPatch) error
}

// reconcile 1 回分の依存をまとめたコンテナ
type Deps struct {
	Store           Store
	Discoverer      port.Discoverer
	Targets         map[model.ResourceType]port.Target
	Notifier        port.Notifier
	DefaultTimezone string
	Log             *slog.Logger
}

// グループ配下で発見されたリソース 1 件の処理結果
type Result struct {
	Group      string              `json:"group,omitempty"`
	ResourceID string              `json:"resource_id,omitempty"`
	Desired    model.DesiredState  `json:"desired,omitempty"`
	Observed   model.ObservedState `json:"observed,omitempty"`
	Action     model.Action        `json:"action,omitempty"`
	Skipped    string              `json:"skipped,omitempty"`
	Error      string              `json:"error,omitempty"`
}

// ハンドラの戻り値
type Summary struct {
	Reconciled int      `json:"reconciled"`
	Actions    []Result `json:"actions"`
	Errors     []Result `json:"errors"`
}

// 緩く型付けした呼び出しペイロードで、`{}`（あるいは任意の JSON オブジェクト）が全体 reconcile を起動する
// 残しているのはログのためだけで、cheapskate はもうこれで挙動を分岐しない
type Event struct {
	Source string `json:"source"`
	Detail struct {
		SourceType       string `json:"SourceType"`
		SourceIdentifier string `json:"SourceIdentifier"`
	} `json:"detail"`
}

// イベントペイロードを解釈して reconcile を実行する
func Run(ctx context.Context, raw json.RawMessage, deps *Deps, now time.Time) (Summary, error) {
	var event Event
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &event); err != nil {
			return Summary{}, fmt.Errorf("unmarshal event: %w", err)
		}
	}
	if event.Source != "" {
		deps.Log.Info("event-received", "source", event.Source, "reason", "every invocation does a full reconcile")
	}

	sr, err := deps.Store.ScanAll(ctx, now)
	if err != nil {
		return Summary{}, err
	}

	claimed := newClaims()

	var results []Result
	for _, row := range sr.Groups {
		if !row.HasGroup {
			deps.Log.Warn("orphaned-group-data", "group", row.Name)
			continue
		}
		results = append(results, ReconcileGroup(ctx, row, claimed, deps, now)...)
	}

	summary := Summary{Reconciled: len(results), Actions: []Result{}, Errors: []Result{}}
	for _, r := range results {
		if r.Action != model.ActionNone {
			summary.Actions = append(summary.Actions, r)
		}
		if r.Error != "" {
			summary.Errors = append(summary.Errors, r)
		}
	}
	deps.Log.Info("summary",
		"reconciled", summary.Reconciled,
		"actions", len(summary.Actions),
		"errors", len(summary.Errors))
	return summary, nil
}

// 「1 リソースは 1 グループだけが管理する」を 1 回の reconcile を通して守る所有権の台帳
//
// セレクタ重複の防護策として、複数グループのセレクタにマッチしたリソースは名前順で最初のグループが取得する
// ScanAll の時点でグループはソート済みなので、所有権は決定的に定まる
// あとに来たグループは黙って二重管理せず、リソース単位のエラーを受け取る
// これは、メンバー登録の時点で「1 リソース 1 グループ」を強制していた旧来の不変条件に代わるものである
// 所属を明示的な登録ではなく探索で算出するようになった以上、書き込み時点で強制する場所はもう存在しない
//
// これがただの map ではなく型なのは、ドメインの規則であって作業用のメモではないからである
// 呼び出し側の map を各グループが書き換えるという形にすると、ReconcileGroup のシグネチャからは「これが出力でもある」ことが読み取れない
type claims struct {
	owner map[string]string // リソース ID -> それを取得したグループ名
}

func newClaims() *claims { return &claims{owner: map[string]string{}} }

// group による resourceID の取得を試みる
// 取得できれば ok が true になる
// すでに他のグループが取得していれば、owner にその名前が入り ok は false になる
func (c *claims) claim(resourceID, group string) (owner string, ok bool) {
	if prev, dup := c.owner[resourceID]; dup {
		return prev, false
	}
	c.owner[resourceID] = group
	return group, true
}

// あるグループのセレクタに現在マッチしているリソースをすべて収束させる
// グループ単位の失敗（不正な mode/cron/timezone/override、Discover の失敗、セレクタの重複）は、リソース単位と同じ recordFailure 経路を通る
// 記録・通知の宛先は合成 resource_id である "group#<name>" になる
// これにより通知の重複排除と last_error の可視性が、新たな管理を一切足さずに機能し続ける
// "group" が実在のリソース種別と衝突することはない（model.KnownTypes を参照）
// disabled のグループは収束させる対象がないので、Discover を呼ばずにスキップする
//
// グループ単位のエラーのクリアは、必ずこの関数の最後に 1 回だけ行う
// Discover 直後にクリアしてからリソースのループでまたグループ単位のエラーを記録すると、「毎サイクル、クリアして通知 → 記録して通知」を永久に繰り返す通知フラップになるためである
func ReconcileGroup(ctx context.Context, row state.GroupRow, claimed *claims, deps *Deps, now time.Time) []Result {
	groupStatusID := model.GroupStatusID(row.Name)

	desired, cfg, err := resolveGroup(row, deps, now)
	if err != nil {
		recordFailure(ctx, deps, row.Name, groupStatusID, err, now)
		return []Result{{Group: row.Name, Error: err.Error()}}
	}
	if desired == model.DesiredNone {
		clearRecoveredError(ctx, deps, row.Name, groupStatusID, row.Status, true, now)
		return []Result{{Group: row.Name, Skipped: "disabled"}}
	}

	resources, err := deps.Discoverer.Discover(ctx, cfg.Selector)
	if err != nil {
		recordFailure(ctx, deps, row.Name, groupStatusID, err, now)
		return []Result{{Group: row.Name, Error: err.Error()}}
	}

	var taken []string // 他のグループがすでに取得済みだったリソース
	results := make([]Result, 0, len(resources))
	for _, res := range resources {
		resourceID := res.ID()
		result := Result{Group: row.Name, ResourceID: resourceID}

		if owner, ok := claimed.claim(resourceID, row.Name); !ok {
			// これはこのグループの設定不備であって、リソースの状態の問題ではない
			// だから記録先は共有の status#<resourceID> ではなく、報告する側のグループのステータスにする
			// 共有アイテムへ書くと、そのリソースを所有するグループの clearRecoveredError と同じ 1 件を毎サイクル奪い合い、通知の重複排除が成立しなくなる
			result.Error = fmt.Sprintf("resource %s also matches group %q's selector; %q claimed it first", resourceID, row.Name, owner)
			taken = append(taken, fmt.Sprintf("%s (claimed by %q)", resourceID, owner))
			results = append(results, result)
			continue
		}

		if err := reconcileResource(ctx, deps, row.Name, res, desired, now, &result); err != nil {
			result.Error = err.Error()
			recordFailure(ctx, deps, row.Name, resourceID, err, now)
		}
		results = append(results, result)
	}

	// 重複は 1 サイクルにつき 1 件のエラーへまとめる
	// リソースごとに記録すると同じグループのステータスアイテムを何度も上書きし、最後の 1 件しか残らない
	// 文言をリソース ID のソート順で決定的にしているのは、内容が変わらない限り再通知しないためである
	if len(taken) > 0 {
		recordFailure(ctx, deps, row.Name, groupStatusID,
			fmt.Errorf("selector overlaps other groups: %s", strings.Join(taken, ", ")), now)
	} else {
		clearRecoveredError(ctx, deps, row.Name, groupStatusID, row.Status, true, now)
	}
	return results
}

func resolveGroup(row state.GroupRow, deps *Deps, now time.Time) (model.DesiredState, model.GroupConfig, error) {
	if row.Err != nil { // このグループ配下の override/group/group-status アイテムが壊れている
		return model.DesiredNone, model.GroupConfig{}, row.Err
	}
	cfg, err := model.ParseGroup(row.Group)
	if err != nil {
		return model.DesiredNone, model.GroupConfig{}, err
	}
	if cfg.Mode == model.ModeDisabled {
		return model.DesiredNone, cfg, nil
	}
	desired, err := schedule.ResolveDesired(cfg, row.Override, now, deps.DefaultTimezone)
	if err != nil {
		return model.DesiredNone, cfg, err
	}
	return desired, cfg, nil
}

// 発見されたリソース 1 件分の処理を行う
// ターゲットを解決し、desired と observed を比較し、食い違えば操作し、永続化して通知する
// いずれかの手順が失敗した時点で中断し、記録は呼び出し側が行う
func reconcileResource(ctx context.Context, deps *Deps, groupName string, res model.Resource, desired model.DesiredState, now time.Time, result *Result) error {
	resourceID := res.ID()

	// 最新を読み、後続のアクション（あれば）と復旧検知の両方で使い回す
	// 1 サイクルあたり GetItem 1 回の追加で、このリソースが直前までエラー状態だったかを知れる
	status, err := deps.Store.GetStatus(ctx, resourceID)
	if err != nil {
		return err
	}

	tgt, ok := deps.Targets[res.Type]
	if !ok {
		return fmt.Errorf("no target for type %q", res.Type)
	}
	obs, err := tgt.Describe(ctx, res.Ref)
	if err != nil {
		return err
	}
	result.Desired, result.Observed = desired, obs.State

	if obs.State == model.StateTransitioning {
		markTransitioning(ctx, deps, resourceID, status, now)
		deps.Log.Info("skip-transitioning", "resource_id", resourceID, "detail", obs.Detail,
			"since", firstNonEmpty(status.TransitioningSince, now.UTC().Format(time.RFC3339)))
		result.Skipped = "transitioning"
		return nil
	}
	clearTransitioning(ctx, deps, resourceID, status)

	if obs.State == model.StateNotFound {
		// 探索直後に消えるリソースは、削除との競合か Tagging API の遅れ（結果は数分ほど実態に遅れる）であり、設定のずれではない
		// 旧来のメンバー登録モデルでは、not-found は明示的に管理を指示されたリソースが登録解除されないまま消えたことを意味していた
		// 探索が追いつけば次のサイクルで自然に解消するので、誰かを叩き起こしてはならない
		deps.Log.Info("skip-not-found", "resource_id", resourceID)
		result.Skipped = "not-found"
		return nil
	}

	action := model.DecideAction(desired, obs.State)
	if action == model.ActionNone {
		// 収束済みなのでアクションの書き込みはしない
		// それでも古いエラーは消し、復旧を 1 度だけ通知する
		// 復旧通知が出るのはここだけである
		// アクションが成功した場合は、その通知自体が正常化を伝えるためである
		clearRecoveredError(ctx, deps, groupName, resourceID, status, true, now)
		return nil // 書き込みもアクション通知もなし
	}

	if err := performAction(ctx, res, action, tgt); err != nil {
		return err
	}
	if err := deps.Store.UpdateStatus(ctx, resourceID, state.StatusPatch{
		ObservedState: state.Set(obs.State),
		LastAction:    state.Set(action),
		LastActionAt:  state.Set(now.UTC().Format(time.RFC3339)),
	}); err != nil {
		return err
	}
	result.Action = action
	deps.Log.Info("action", "group", groupName, "resource_id", resourceID, "action", action, "desired", desired)

	notifyAction(ctx, deps, groupName, resourceID, action, desired, now)
	clearRecoveredError(ctx, deps, groupName, resourceID, status, false, now)
	return nil
}

// 遷移が始まった時刻を、それがまだ記録されていない場合に限って残す
// 遷移中のリソースは毎サイクル skip されるので、無条件に書くと定常状態の書き込みを避けた設計が崩れる
// 記録済みならこのサイクルは書き込みなしで通り抜ける
// 失敗してもベストエフォートで済ませる
// これは監査のための情報であり、これが書けなかったことを理由に収束を止める価値はない
func markTransitioning(ctx context.Context, deps *Deps, resourceID string, prevStatus model.Status, now time.Time) {
	if prevStatus.TransitioningSince != "" {
		return
	}
	if err := deps.Store.UpdateStatus(ctx, resourceID, state.StatusPatch{
		TransitioningSince: state.Set(now.UTC().Format(time.RFC3339)),
	}); err != nil {
		deps.Log.Error("transitioning-mark-failed", "resource_id", resourceID, "error", err.Error())
	}
}

// 遷移でない状態（running / stopped / not-found）を観測したので、記録してあった遷移の開始時刻を消す
// markTransitioning と同じくベストエフォートである
// 消し損ねても、次に遷移でない状態を観測したサイクルがまた消しにいく
func clearTransitioning(ctx context.Context, deps *Deps, resourceID string, prevStatus model.Status) {
	if prevStatus.TransitioningSince == "" {
		return
	}
	if err := deps.Store.UpdateStatus(ctx, resourceID, state.StatusPatch{TransitioningSince: state.Set("")}); err != nil {
		deps.Log.Error("transitioning-clear-failed", "resource_id", resourceID, "error", err.Error())
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// AWS 側の変更操作を実行する
// model.DecideAction が返しうるのは ActionStop・ActionStart・ActionNone だけであり、ActionNone は呼び出し側で先に分岐している
// それでも最後の 1 行を残してあるのは、model.Action に値を足したときここを直し忘れたら
// 黙って何もしないのではなく失敗するようにするためである（doctor の pruners と同じ考え方）
func performAction(ctx context.Context, res model.Resource, action model.Action, tgt port.Target) error {
	switch action {
	case model.ActionStop:
		return tgt.Stop(ctx, res.Ref)
	case model.ActionStart:
		return tgt.Start(ctx, res)
	}
	return fmt.Errorf("unknown action %q", action)
}

// ベストエフォートで通知する
// 通知の失敗はログに残すだけで、reconcile のエラーとしては扱わない
// アクション自体はすでに成功し、永続化も済んでいるためである
func notifyAction(ctx context.Context, deps *Deps, group, resourceID string, action model.Action, desired model.DesiredState, now time.Time) {
	if err := deps.Notifier.Publish(ctx,
		fmt.Sprintf("[cheapskate] %s: %s/%s", action, group, resourceID),
		map[string]any{"group": group, "resource_id": resourceID, "action": action, "desired": desired, "at": now.UTC().Format(time.RFC3339)},
	); err != nil {
		deps.Log.Error("action-notify-failed", "group", group, "resource_id", resourceID, "error", err.Error())
	}
}

// エラーなしでサイクルが完了したら、以前に記録されたエラーを消す
// notify は「復旧」通知を別途送るかどうかを制御する
// アクションが成功した直後は呼び出し側が通知を省く
// アクション通知がすでに復旧を伝えているためである
func clearRecoveredError(ctx context.Context, deps *Deps, group, resourceID string, prevStatus model.Status, notify bool, now time.Time) {
	if prevStatus.LastError == "" {
		return
	}
	if err := deps.Store.UpdateStatus(ctx, resourceID, state.StatusPatch{
		LastError:   state.Set(""),
		LastErrorAt: state.Set(""),
	}); err != nil {
		deps.Log.Error("error-clear-failed", "group", group, "resource_id", resourceID, "error", err.Error())
		return
	}
	if !notify {
		return
	}
	if err := deps.Notifier.Publish(ctx,
		fmt.Sprintf("[cheapskate] recovered: %s/%s", group, resourceID),
		map[string]any{"group": group, "resource_id": resourceID, "at": now.UTC().Format(time.RFC3339)},
	); err != nil {
		deps.Log.Error("recovery-notify-failed", "group", group, "resource_id", resourceID, "error", err.Error())
	}
}

// エラーは無条件に永続化するが、通知するのは以前に記録したものと内容が違うときだけである
// そうしないと、継続する not-found や access-denied のエラーが毎サイクル永遠に呼び出しを鳴らし続ける
func recordFailure(ctx context.Context, deps *Deps, group, resourceID string, err error, now time.Time) {
	deps.Log.Error("error", "group", group, "resource_id", resourceID, "error", err.Error())
	at := now.UTC().Format(time.RFC3339)

	prevStatus, gerr := deps.Store.GetStatus(ctx, resourceID)
	if gerr != nil {
		deps.Log.Error("status-read-failed", "group", group, "resource_id", resourceID, "error", gerr.Error())
	}

	if serr := deps.Store.UpdateStatus(ctx, resourceID, state.StatusPatch{
		LastError:   state.Set(err.Error()),
		LastErrorAt: state.Set(at),
	}); serr != nil {
		deps.Log.Error("error-record-failed", "group", group, "resource_id", resourceID, "error", serr.Error())
	}

	if gerr == nil && prevStatus.LastError == err.Error() {
		return
	}
	if nerr := deps.Notifier.Publish(ctx,
		fmt.Sprintf("[cheapskate] error: %s/%s", group, resourceID),
		map[string]any{"group": group, "resource_id": resourceID, "error": err.Error(), "at": at},
	); nerr != nil {
		deps.Log.Error("error-notify-failed", "group", group, "resource_id", resourceID, "error", nerr.Error())
	}
}
