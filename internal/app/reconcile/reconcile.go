// reconcile ループを実装する
// スケジュール起動と RDS イベント起動のいずれでも、呼び出しごとに全体を reconcile する
// 単一リソースへ絞る reconcile は、グループ所属が静的なメンバー登録から AWS タグの動的探索へ移行した時点で廃止した
// 絞り込みを行っても、全体 reconcile と同じコストになるためである
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
// *state.Store が満たすが、reconciler が受け取るのはこの範囲に限る
//
// グループ設定と override を書くメソッドは含めない
// それらを所有するのは CLI と web console であり、reconciler にとっては読み取り専用の入力である
// 型で限定することにより、reconcile から設定を書き換える経路が存在しなくなる
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

// 呼び出しペイロードであり、任意の JSON オブジェクトが全体 reconcile を起動する
// 用途はログへの記録に限り、内容による処理の分岐は行わない
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

// 1 つのリソースを 1 つのグループのみが管理するという規則を、1 回の reconcile を通して保つ所有権の台帳
//
// セレクタが重複した場合、複数グループのセレクタに一致したリソースは名前順で最初のグループが取得する
// ScanAll の時点でグループはソート済みであるため、所有権は決定的に定まる
// 以降のグループは二重の管理を行わず、リソース単位のエラーを受け取る
// これは、メンバー登録の時点で 1 リソース 1 グループを強制していた旧来の不変条件に代わるものである
// 所属を登録ではなく探索で算出するため、書き込み時点で強制する箇所は存在しない
//
// map ではなく型として宣言するのは、これがドメインの規則であるためである
// 呼び出し側の map を各グループが書き換える形とした場合、ReconcileGroup のシグネチャから出力であることを読み取れない
type claims struct {
	owner map[string]string // リソース ID -> それを取得したグループ名
}

func newClaims() *claims { return &claims{owner: map[string]string{}} }

// group による resourceID の取得を試みる
// 取得できた場合は ok が true となる
// 他のグループが取得済みの場合は、owner にその名前が入り ok は false となる
func (c *claims) claim(resourceID, group string) (owner string, ok bool) {
	if prev, dup := c.owner[resourceID]; dup {
		return prev, false
	}
	c.owner[resourceID] = group
	return group, true
}

// あるグループのセレクタに現在一致するリソースをすべて収束させる
// グループ単位の失敗は、リソース単位と同じ recordFailure 経路を通る
// 該当するのは不正な mode/cron/timezone/override、Discover の失敗、セレクタの重複である
// 記録と通知の宛先は、合成 resource_id である "group#<name>" となる
// これにより、通知の重複排除と last_error の可視性が追加の機構なしに成立する
// "group" が実在のリソース種別と衝突することはない (model.KnownTypes を参照)
// disabled のグループは収束の対象を持たないため、Discover を呼ばずにスキップする
//
// グループ単位のエラーのクリアは、この関数の最後に 1 回だけ行う
// Discover の直後にクリアし、リソースのループで再度グループ単位のエラーを記録した場合、クリアの通知と記録の通知を毎サイクル繰り返すためである
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
			// これはグループの設定不備であり、リソースの状態の問題ではない
			// 記録先は共有の status#<resourceID> ではなく、報告する側のグループのステータスとする
			// 共有アイテムへ書いた場合、そのリソースを所有するグループの clearRecoveredError と同じアイテムを毎サイクル更新し、通知の重複排除が成立しなくなる
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
	// リソースごとに記録した場合、同じグループのステータスアイテムを繰り返し上書きし、最後の 1 件のみが残る
	// 文言をリソース ID のソート順で決定的にするのは、内容が変わらない限り再通知しないためである
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

// 発見されたリソース 1 件を処理する
// ターゲットを解決し、desired と observed を比較し、差異があれば操作し、永続化して通知する
// いずれかの手順が失敗した時点で中断する。記録は呼び出し側が行う
func reconcileResource(ctx context.Context, deps *Deps, groupName string, res model.Resource, desired model.DesiredState, now time.Time, result *Result) error {
	resourceID := res.ID()

	// 最新のステータスを読み、後続のアクションと復旧検知の双方で用いる
	// 1 サイクルあたり GetItem 1 回の追加により、直前までエラー状態であったかを判定できる
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
		// 探索の直後に消えるリソースは、削除との競合または Tagging API の反映遅延によるものであり、設定の不整合ではない
		// 旧来のメンバー登録モデルでは、not-found は登録済みのリソースが登録解除されないまま消えたことを意味していた
		// 探索の反映により次のサイクルで解消するため、通知は行わない
		deps.Log.Info("skip-not-found", "resource_id", resourceID)
		result.Skipped = "not-found"
		return nil
	}

	action := model.DecideAction(desired, obs.State)
	if action == model.ActionNone {
		// 収束済みであるため、アクションの書き込みは行わない
		// 過去のエラーは削除し、復旧を 1 度だけ通知する
		// 復旧通知を行うのはこの経路のみである
		// アクションが成功した場合は、その通知が正常化を伝えるためである
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

// 遷移の開始時刻を、未記録の場合に限り記録する
// 遷移中のリソースは毎サイクル skip されるため、無条件の書き込みは定常状態における書き込みの抑制に反する
// 記録済みの場合、このサイクルでは書き込みを行わない
// 書き込みの失敗はベストエフォートとして扱う
// これは監査のための情報であり、その書き込みの失敗を理由に収束を中断しない
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

// 遷移でない状態(running / stopped / not-found)を観測したので、記録してあった遷移の開始時刻を消す
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
// model.DecideAction が返すのは ActionStop、ActionStart、ActionNone に限り、ActionNone は呼び出し側で分岐する
// 最後の節は、model.Action へ値を追加してここを更新しなかった場合に、
// 何も行わずに成功するのではなく失敗させるために存在する (doctor の pruners と同じ方針である)
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
