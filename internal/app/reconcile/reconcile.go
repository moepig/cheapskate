// reconcile ループを実装する
// スケジュール起動と RDS イベント起動のいずれでも、呼び出しごとに全体を reconcile する
// 単一リソースへ絞る reconcile は、グループ所属が静的なメンバー登録から AWS タグの動的探索へ移行した時点で廃止した
// 絞り込みを行っても、全体 reconcile と同じコストになるためである
package reconcile

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	AcquireLease(ctx context.Context, owner string, now, expiresAt time.Time) (bool, error)
	ReleaseLease(ctx context.Context, owner string) error
	ListGroups(ctx context.Context, now time.Time) ([]state.GroupRow, error)
	GetStatuses(ctx context.Context, resourceIDs []string) (map[string]state.StatusRecord, error)
	UpdateStatus(ctx context.Context, resourceID string, p state.StatusPatch) error
	BeginOperation(ctx context.Context, resourceID string, op state.PendingOperation) error
	CompleteOperation(ctx context.Context, resourceID string, op state.PendingOperation) error
	AbandonOperation(ctx context.Context, resourceID, operationID string) error
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
	Skipped    string   `json:"skipped,omitempty"`
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

	owner, err := newOperationID()
	if err != nil {
		return Summary{}, fmt.Errorf("create reconcile lease owner: %w", err)
	}
	acquired, err := deps.Store.AcquireLease(ctx, owner, now, leaseExpiration(ctx, now))
	if err != nil {
		return Summary{}, err
	}
	if !acquired {
		deps.Log.Info("skip-lease-held")
		return Summary{Actions: []Result{}, Errors: []Result{}, Skipped: "lease-held"}, nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := deps.Store.ReleaseLease(releaseCtx, owner); err != nil {
			deps.Log.Error("lease-release-failed", "owner", owner, "error", err.Error())
		}
	}()

	rows, err := deps.Store.ListGroups(ctx, now)
	if err != nil {
		return Summary{}, err
	}

	var prepared []preparedGroup
	ownershipBarrier := -1
	for _, row := range rows {
		if !row.HasGroup {
			deps.Log.Warn("orphaned-group-data", "group", row.Name)
			continue
		}
		group := prepareGroup(ctx, row, deps, now)
		if group.discoverErr && ownershipBarrier < 0 {
			ownershipBarrier = len(prepared)
		}
		prepared = append(prepared, group)
	}

	claimed := newClaims()
	claimLimit := len(prepared)
	if ownershipBarrier >= 0 {
		claimLimit = ownershipBarrier
	}
	for i := range claimLimit {
		group := prepared[i]
		if group.err != nil || group.desired == model.DesiredNone {
			continue
		}
		for _, resource := range group.resources {
			claimed.claim(resource.ID(), group.row.Name)
		}
	}

	var results []Result
	for i, group := range prepared {
		if ownershipBarrier >= 0 && i > ownershipBarrier && group.err == nil && group.desired != model.DesiredNone {
			barrierGroup := prepared[ownershipBarrier].row.Name
			err := fmt.Errorf("resource ownership is unknown because discovery failed for earlier group %q", barrierGroup)
			recordFailure(ctx, deps, group.row.Name, model.GroupStatusID(group.row.Name), group.row.Status, err, now)
			results = append(results, Result{Group: group.row.Name, Error: err.Error()})
			continue
		}
		results = append(results, reconcilePreparedGroup(ctx, group, claimed, deps, now)...)
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

const (
	defaultLeaseDuration    = 3 * time.Minute
	leaseSafetyMargin       = 15 * time.Second
	pendingRecoveryAfter    = 30 * time.Minute
	maxNotificationAttempts = 2
)

func leaseExpiration(ctx context.Context, now time.Time) time.Time {
	deadline, ok := ctx.Deadline()
	if !ok {
		return now.Add(defaultLeaseDuration)
	}
	remaining := max(time.Until(deadline), 0)
	return now.Add(remaining + leaseSafetyMargin)
}

func newOperationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// 1 つのリソースを 1 つのグループのみが管理するという規則を、1 回の reconcile を通して保つ所有権の台帳
//
// セレクタが重複した場合、複数グループのセレクタに一致したリソースは名前順で最初のグループが取得する
// ListGroups の時点でグループはソート済みであるため、所有権は決定的に定まる
// 以降のグループは二重の管理を行わず、リソース単位のエラーを受け取る
// これは、メンバー登録の時点で 1 リソース 1 グループを強制していた旧来の不変条件に代わるものである
// 所属を登録ではなく探索で算出するため、書き込み時点で強制する箇所は存在しない
//
// map ではなく型として宣言するのは、これがドメインの規則であるためである。
// 探索フェーズで台帳を完成させてから、操作フェーズが読み取る。
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

func (c *claims) ownerOf(resourceID string) (string, bool) {
	owner, ok := c.owner[resourceID]
	return owner, ok
}

type preparedGroup struct {
	row         state.GroupRow
	desired     model.DesiredState
	cfg         model.GroupConfig
	resources   []model.Resource
	err         error
	discoverErr bool
}

// 設定の解決と探索だけを行い、Status の更新、Describe、および AWS 操作は行わない。
func prepareGroup(ctx context.Context, row state.GroupRow, deps *Deps, now time.Time) preparedGroup {
	group := preparedGroup{row: row}
	if row.StatusErr != nil {
		deps.Log.Warn("group-status-corrupt", "group", row.Name, "error", row.StatusErr.Error())
	}

	group.desired, group.cfg, group.err = resolveGroup(row, deps, now)
	if group.err != nil || group.desired == model.DesiredNone {
		return group
	}
	group.resources, group.err = deps.Discoverer.Discover(ctx, group.cfg.Selector)
	group.discoverErr = group.err != nil
	return group
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
func reconcilePreparedGroup(ctx context.Context, group preparedGroup, claimed *claims, deps *Deps, now time.Time) []Result {
	row := group.row
	groupStatusID := model.GroupStatusID(row.Name)
	if group.err != nil {
		recordFailure(ctx, deps, row.Name, groupStatusID, row.Status, group.err, now)
		return []Result{{Group: row.Name, Error: group.err.Error()}}
	}
	if group.desired == model.DesiredNone {
		clearRecoveredError(ctx, deps, row.Name, groupStatusID, row.Status, now)
		return []Result{{Group: row.Name, Skipped: "disabled"}}
	}

	ids := make([]string, 0, len(group.resources))
	for _, resource := range group.resources {
		ids = append(ids, resource.ID())
	}
	statuses, err := deps.Store.GetStatuses(ctx, ids)
	if err != nil {
		recordFailure(ctx, deps, row.Name, groupStatusID, row.Status, err, now)
		return []Result{{Group: row.Name, Error: err.Error()}}
	}

	var taken []string // 他のグループがすでに取得済みだったリソース
	scope := newOperationScope(group.cfg, row.Override, group.desired, deps.DefaultTimezone)
	results := make([]Result, 0, len(group.resources))
	for _, res := range group.resources {
		resourceID := res.ID()
		result := Result{Group: row.Name, ResourceID: resourceID}

		owner, owned := claimed.ownerOf(resourceID)
		if !owned {
			result.Error = fmt.Sprintf("resource %s has no owner after discovery", resourceID)
			results = append(results, result)
			continue
		}
		if owner != row.Name {
			// これはグループの設定不備であり、リソースの状態の問題ではない
			// 記録先は共有の status#<resourceID> ではなく、報告する側のグループのステータスとする
			// 共有アイテムへ書いた場合、そのリソースを所有するグループの clearRecoveredError と同じアイテムを毎サイクル更新し、通知の重複排除が成立しなくなる
			result.Error = fmt.Sprintf("resource %s also matches group %q's selector; %q claimed it first", resourceID, row.Name, owner)
			taken = append(taken, fmt.Sprintf("%s (claimed by %q)", resourceID, owner))
			results = append(results, result)
			continue
		}

		record := statuses[resourceID]
		if record.Err != nil && record.PendingCorrupt() {
			result.Error = record.Err.Error()
			recordFailure(ctx, deps, row.Name, resourceID, record.Status, record.Err, now)
			results = append(results, result)
			continue
		}
		if record.Err != nil {
			deps.Log.Warn("resource-status-corrupt", "group", row.Name, "resource_id", resourceID, "error", record.Err.Error())
		}
		if err := reconcileResource(ctx, deps, scope, res, group.desired, record.Status, now, &result); err != nil {
			result.Error = err.Error()
			recordFailure(ctx, deps, result.Group, resourceID, record.Status, err, now)
		}
		results = append(results, result)
	}

	// 重複は 1 サイクルにつき 1 件のエラーへまとめる
	// リソースごとに記録した場合、同じグループのステータスアイテムを繰り返し上書きし、最後の 1 件のみが残る
	// 文言をリソース ID のソート順で決定的にするのは、内容が変わらない限り再通知しないためである
	if len(taken) > 0 {
		recordFailure(ctx, deps, row.Name, groupStatusID, row.Status,
			fmt.Errorf("selector overlaps other groups: %s", strings.Join(taken, ", ")), now)
	} else {
		clearRecoveredError(ctx, deps, row.Name, groupStatusID, row.Status, now)
	}
	return results
}

// AWS 操作を開始したグループと、その時点で有効だった設定を識別する。
// 動的なセレクタによりリソースの所属が変わっても、未完了操作の通知先と設定境界を保持する。
type operationScope struct {
	Group      string
	ConfigHash string
}

// 操作結果に影響する設定を正規化し、同じ設定からは同じ値を生成する。
func newOperationScope(cfg model.GroupConfig, override *model.Override, desired model.DesiredState, defaultTimezone string) operationScope {
	payload := struct {
		Group             string
		Mode              model.Mode
		ConfiguredDesired model.DesiredState
		ResolvedDesired   model.DesiredState
		StartCron         string
		StopCron          string
		Timezone          string
		DefaultTimezone   string
		TagKey            string
		TagValue          string
		Types             []string
		OverrideDesired   model.DesiredState
		OverrideExpiresAt int64
	}{
		Group: cfg.Name, Mode: cfg.Mode, ConfiguredDesired: cfg.Desired, ResolvedDesired: desired,
		StartCron: cfg.StartCron, StopCron: cfg.StopCron, Timezone: cfg.Timezone, DefaultTimezone: defaultTimezone,
		TagKey: cfg.Selector.TagKey, TagValue: cfg.Selector.TagValue, Types: model.TypeNames(cfg.Selector.Types),
	}
	if override != nil {
		payload.OverrideDesired = override.Desired
		payload.OverrideExpiresAt = override.ExpiresAt
	}
	raw, _ := json.Marshal(payload)
	hash := sha256.Sum256(raw)
	return operationScope{Group: cfg.Name, ConfigHash: hex.EncodeToString(hash[:])}
}

func resolveGroup(row state.GroupRow, deps *Deps, now time.Time) (model.DesiredState, model.GroupConfig, error) {
	if row.GroupErr != nil {
		return model.DesiredNone, model.GroupConfig{}, row.GroupErr
	}
	if row.OverrideErr != nil {
		return model.DesiredNone, model.GroupConfig{}, row.OverrideErr
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
func reconcileResource(ctx context.Context, deps *Deps, scope operationScope, res model.Resource, desired model.DesiredState, status model.Status, now time.Time, result *Result) error {
	resourceID := res.ID()
	groupName := scope.Group

	tgt, ok := deps.Targets[res.Type]
	if !ok {
		return fmt.Errorf("no target for type %q", res.Type)
	}
	obs, err := tgt.Describe(ctx, res.Ref)
	if err != nil {
		return err
	}
	result.Desired, result.Observed = desired, obs.State

	if status.PendingOperationID != "" {
		op, startedAt, err := pendingOperation(status)
		if err != nil {
			return err
		}
		pendingGroup := firstNonEmpty(op.Group, groupName)
		result.Group = pendingGroup
		if observationMatchesDesired(obs.State, op.Desired) {
			if err := deps.Store.CompleteOperation(ctx, resourceID, op); err != nil {
				return fmt.Errorf("complete recovered operation %s: %w", op.ID, err)
			}
			contextChanged := op.Group != "" && (op.Group != scope.Group || op.ConfigHash != scope.ConfigHash)
			result.Action, result.Desired = op.Action, op.Desired
			deps.Log.Info("action-recovered", "group", pendingGroup, "resource_id", resourceID,
				"operation_id", op.ID, "action", op.Action, "desired", op.Desired)
			deliverActionNotification(ctx, deps, pendingGroup, resourceID, op.ID, op.Action, op.Desired, op.StartedAt)
			status.PendingOperationID = ""
			status.PendingGroup = ""
			status.PendingConfigHash = ""
			status.PendingAction = model.ActionNone
			status.PendingDesired = model.DesiredNone
			status.PendingObserved = ""
			status.PendingStartedAt = ""
			status.LastAction = op.Action
			status.LastDesired = op.Desired
			status.LastActionAt = op.StartedAt
			status.LastError = ""
			status.LastErrorAt = ""
			if contextChanged {
				deps.Log.Info("pending-operation-context-changed", "resource_id", resourceID,
					"operation_id", op.ID, "pending_group", op.Group, "current_group", scope.Group,
					"pending_config_hash", op.ConfigHash, "current_config_hash", scope.ConfigHash)
				result.Skipped = "operation-context-changed"
				return nil
			}
		} else if obs.State == model.StateTransitioning {
			markTransitioning(ctx, deps, resourceID, status, now)
			deps.Log.Info("skip-pending-transition", "resource_id", resourceID, "operation_id", op.ID,
				"detail", obs.Detail, "since", firstNonEmpty(status.TransitioningSince, now.UTC().Format(time.RFC3339)))
			result.Skipped = "pending-action"
			return nil
		} else if now.Sub(startedAt) < pendingRecoveryAfter {
			deps.Log.Info("skip-pending-action", "resource_id", resourceID, "operation_id", op.ID,
				"observed", obs.State, "age", now.Sub(startedAt))
			result.Skipped = "pending-action"
			return nil
		} else {
			if err := deps.Store.AbandonOperation(ctx, resourceID, op.ID); err != nil {
				return fmt.Errorf("abandon stale operation %s: %w", op.ID, err)
			}
			return fmt.Errorf("operation %s did not converge within %s", op.ID, pendingRecoveryAfter)
		}
	}

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
		clearRecoveredError(ctx, deps, groupName, resourceID, status, now)
		return nil // 書き込みもアクション通知もなし
	}

	operationID, err := newOperationID()
	if err != nil {
		return fmt.Errorf("create operation ID: %w", err)
	}
	op := state.PendingOperation{
		ID: operationID, Group: scope.Group, ConfigHash: scope.ConfigHash,
		Action: action, Desired: desired, Observed: obs.State,
		StartedAt: now.UTC().Format(time.RFC3339),
	}
	if err := deps.Store.BeginOperation(ctx, resourceID, op); err != nil {
		return fmt.Errorf("record pending operation %s: %w", operationID, err)
	}
	if err := performAction(ctx, res, action, tgt); err != nil {
		if abandonErr := deps.Store.AbandonOperation(ctx, resourceID, operationID); abandonErr != nil {
			deps.Log.Error("pending-operation-abandon-failed", "resource_id", resourceID,
				"operation_id", operationID, "error", abandonErr.Error())
		}
		return err
	}
	result.Action = action
	deps.Log.Info("action", "group", groupName, "resource_id", resourceID, "operation_id", operationID,
		"action", action, "desired", desired)
	if err := deps.Store.CompleteOperation(ctx, resourceID, op); err != nil {
		return fmt.Errorf("complete operation %s: %w", operationID, err)
	}

	deliverActionNotification(ctx, deps, groupName, resourceID, operationID, action, desired, op.StartedAt)
	return nil
}

func pendingOperation(status model.Status) (state.PendingOperation, time.Time, error) {
	op := state.PendingOperation{
		ID: status.PendingOperationID, Group: status.PendingGroup, ConfigHash: status.PendingConfigHash,
		Action: status.PendingAction, Desired: status.PendingDesired,
		Observed: status.PendingObserved, StartedAt: status.PendingStartedAt,
	}
	if (op.Group == "") != (op.ConfigHash == "") {
		return state.PendingOperation{}, time.Time{}, fmt.Errorf("pending operation %s has incomplete ownership context", op.ID)
	}
	if op.Group != "" {
		if err := model.ValidGroupName(op.Group); err != nil {
			return state.PendingOperation{}, time.Time{}, fmt.Errorf("pending operation %s has invalid group: %w", op.ID, err)
		}
		hash, err := hex.DecodeString(op.ConfigHash)
		if err != nil || len(hash) != sha256.Size {
			return state.PendingOperation{}, time.Time{}, fmt.Errorf("pending operation %s has invalid config hash %q", op.ID, op.ConfigHash)
		}
	}
	startedAt, err := time.Parse(time.RFC3339, op.StartedAt)
	if err != nil {
		return state.PendingOperation{}, time.Time{}, fmt.Errorf("pending operation %s has invalid started_at: %w", op.ID, err)
	}
	if op.Action != model.ActionStart && op.Action != model.ActionStop {
		return state.PendingOperation{}, time.Time{}, fmt.Errorf("pending operation %s has invalid action %q", op.ID, op.Action)
	}
	if err := op.Desired.Validate(); err != nil {
		return state.PendingOperation{}, time.Time{}, fmt.Errorf("pending operation %s has invalid desired state: %w", op.ID, err)
	}
	if op.Observed != model.StateRunning && op.Observed != model.StateStopped {
		return state.PendingOperation{}, time.Time{}, fmt.Errorf("pending operation %s has invalid observed state %q", op.ID, op.Observed)
	}
	if model.DecideAction(op.Desired, op.Observed) != op.Action {
		return state.PendingOperation{}, time.Time{}, fmt.Errorf("pending operation %s has inconsistent action %q", op.ID, op.Action)
	}
	return op, startedAt, nil
}

func observationMatchesDesired(observed model.ObservedState, desired model.DesiredState) bool {
	return observed == model.ObservedState(desired)
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
		TransitioningSince: new(now.UTC().Format(time.RFC3339)),
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
	if err := deps.Store.UpdateStatus(ctx, resourceID, state.StatusPatch{TransitioningSince: new("")}); err != nil {
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

// 完了済みの操作を通知する。
func deliverActionNotification(
	ctx context.Context,
	deps *Deps,
	group, resourceID, operationID string,
	action model.Action,
	desired model.DesiredState,
	at string,
) {
	publishNotification(ctx, deps, "action-notify",
		fmt.Sprintf("[cheapskate] %s: %s/%s", action, group, resourceID),
		map[string]any{
			"group": group, "resource_id": resourceID, "operation_id": operationID,
			"action": action, "desired": desired, "at": at,
		},
		"group", group, "resource_id", resourceID, "operation_id", operationID)
}

// 同じ本文を最大 maxNotificationAttempts 回送信する。
// すべて失敗しても呼び出し元の処理は失敗させない。
func publishNotification(ctx context.Context, deps *Deps, logEvent, subject string, payload map[string]any, logAttrs ...any) {
	for attempt := 1; attempt <= maxNotificationAttempts; attempt++ {
		err := deps.Notifier.Publish(ctx, subject, payload)
		if err == nil {
			return
		}
		attrs := append([]any{}, logAttrs...)
		attrs = append(attrs, "attempt", attempt, "max_attempts", maxNotificationAttempts, "error", err.Error())
		deps.Log.Error(logEvent+"-failed", attrs...)
	}
	attrs := append([]any{}, logAttrs...)
	attrs = append(attrs, "attempts", maxNotificationAttempts)
	deps.Log.Error(logEvent+"-abandoned", attrs...)
}

// アクションを伴わずに正常化した場合、以前のエラーを解除して復旧を通知する。
func clearRecoveredError(ctx context.Context, deps *Deps, group, resourceID string, prevStatus model.Status, now time.Time) {
	if prevStatus.LastError == "" {
		return
	}
	if err := deps.Store.UpdateStatus(ctx, resourceID, state.StatusPatch{
		LastError:   new(""),
		LastErrorAt: new(""),
	}); err != nil {
		deps.Log.Error("error-clear-failed", "group", group, "resource_id", resourceID, "error", err.Error())
		return
	}
	publishNotification(ctx, deps, "recovery-notify",
		fmt.Sprintf("[cheapskate] recovered: %s/%s", group, resourceID),
		map[string]any{"group": group, "resource_id": resourceID, "at": now.UTC().Format(time.RFC3339)},
		"group", group, "resource_id", resourceID)
}

// エラーは無条件に永続化するが、通知するのは以前に記録したものと内容が違うときだけである
// そうしないと、継続する not-found や access-denied のエラーが毎サイクル永遠に呼び出しを鳴らし続ける
func recordFailure(ctx context.Context, deps *Deps, group, resourceID string, prevStatus model.Status, err error, now time.Time) {
	deps.Log.Error("error", "group", group, "resource_id", resourceID, "error", err.Error())
	at := now.UTC().Format(time.RFC3339)

	if serr := deps.Store.UpdateStatus(ctx, resourceID, state.StatusPatch{
		LastError:   new(err.Error()),
		LastErrorAt: new(at),
	}); serr != nil {
		deps.Log.Error("error-record-failed", "group", group, "resource_id", resourceID, "error", serr.Error())
	}

	if prevStatus.LastError == err.Error() {
		return
	}
	publishNotification(ctx, deps, "error-notify",
		fmt.Sprintf("[cheapskate] error: %s/%s", group, resourceID),
		map[string]any{"group": group, "resource_id": resourceID, "error": err.Error(), "at": at},
		"group", group, "resource_id", resourceID)
}
