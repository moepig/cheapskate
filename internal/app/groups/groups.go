// cheapskate-cli と web console が共有する設定操作を実装する
// 操作対象は DynamoDB のアイテムと読み取り専用の tag:GetResources API に限る
// RDS/ECS/EC2 のコントロール API は呼ばない
//
// 設定の変更規則そのものは持たない
// 各モードへの遷移は model.GroupSpec のメソッドが実装し、本パッケージはその前後の読み書きを担う
// 遷移側が結果を検証してから返すため、本層が model.ParseGroup に拒否される設定を保存することはない
package groups

import (
	"context"
	"fmt"
	"time"

	"cheapskate/internal/app/port"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
)

// 設定フロントエンドが state テーブルに求める範囲
// *state.Store が満たすが、設定操作が受け取るのはこの範囲に限る
//
// UpdateStatus は含めない
// status# アイテムを所有するのは reconciler であり、CLI と web console にとっては読み取り専用である
// 型で限定することにより、設定操作から監査証跡を書き換える経路が存在しなくなる
type Store interface {
	ScanAll(ctx context.Context, now time.Time) (state.ScanResult, error)
	GetGroup(ctx context.Context, name string) (*model.GroupSpec, error)
	PutGroup(ctx context.Context, spec model.GroupSpec) error
	PutOverride(ctx context.Context, group string, o model.Override) error
	DeleteGroup(ctx context.Context, name string) error
	DeleteOverride(ctx context.Context, name string) error
	DeleteGroupStatus(ctx context.Context, name string) error
}

// ターゲットグループ 1 件と、その override およびグループ単位のステータス
// 1 回の ScanAll から組み立て、リソースの動的探索は行わない
// List を Scan 1 回に保つためであり、探索は GetDetail が 1 グループずつ行う
// override、group#、group-status のいずれかが壊れている場合は Err を設定する
// この場合も行自体は返し、一覧全体を失敗させない
type GroupRow struct {
	Name     string
	Group    model.GroupSpec
	Override *model.Override
	Status   model.Status
	Err      error
}

// 登録済みの全グループを、override とグループ単位のステータスを解決した状態で返す
// グループごとの GetItem ではなく、Scan 1 回 (state.ScanAll) で取得する
// override やステータスが存在し group# アイテムが存在しない名前は孤立データとみなし、結果に含めない
func List(ctx context.Context, s Store, now time.Time) ([]GroupRow, error) {
	sr, err := s.ScanAll(ctx, now)
	if err != nil {
		return nil, err
	}
	rows := make([]GroupRow, 0, len(sr.Groups))
	for _, gr := range sr.Groups {
		if !gr.HasGroup {
			continue
		}
		rows = append(rows, toGroupRow(gr))
	}
	return rows, nil
}

func toGroupRow(gr state.GroupRow) GroupRow {
	return GroupRow{Name: gr.Name, Group: gr.Group, Override: gr.Override, Status: gr.Status, Err: gr.Err}
}

// グループのセレクタに現在一致するリソース 1 件と、そのステータス
// 種別に対応する port.Describer が結線されている場合は、現在の状態のスナップショットを併せ持つ
// 種別に対応する Describer が存在しない場合、および Describe の呼び出しが失敗した場合、Live は nil となる (LiveErr を参照)
// いずれの場合も状態を不明として扱い、行全体のエラーとはしない
type ResourceRow struct {
	Resource model.Resource
	Status   model.Status
	Live     *model.Observation
	LiveErr  error
}

// グループの詳細であり、設定、override、グループ単位のステータスを含む
// セレクタが設定されている場合は、現在一致する全リソースを動的に探索し、リソース単位のステータスと結合する
// Discover の失敗はエラーではなくデータとして DiscoverErr へ格納する
// これにより tag:GetResources の権限不足は、ページやコマンド全体の失敗とならない
type GroupDetail struct {
	Name        string
	Group       model.GroupSpec
	Override    *model.Override
	Status      model.Status
	Err         error
	Resources   []ResourceRow
	DiscoverErr error
}

// グループ 1 件の詳細を解決する
// セレクタが存在する場合はメンバーを動的に探索し、describers[member.Type] が存在するメンバーについて現在の状態を問い合わせる
// マップが nil または空の場合、各行の Live は nil のままとなる
func GetDetail(ctx context.Context, s Store, d port.Discoverer, describers map[model.ResourceType]port.Describer, group string, now time.Time) (GroupDetail, error) {
	if err := model.ValidGroupName(group); err != nil {
		return GroupDetail{}, err
	}
	sr, err := s.ScanAll(ctx, now)
	if err != nil {
		return GroupDetail{}, err
	}
	var row *state.GroupRow
	for i := range sr.Groups {
		if sr.Groups[i].Name == group && sr.Groups[i].HasGroup {
			row = &sr.Groups[i]
			break
		}
	}
	if row == nil {
		return GroupDetail{}, fmt.Errorf("group %q is not registered", group)
	}
	detail := GroupDetail{Name: row.Name, Group: row.Group, Override: row.Override, Status: row.Status, Err: row.Err}

	cfg, perr := model.ParseGroup(row.Group)
	if perr != nil || cfg.Selector.Empty() {
		return detail, nil
	}
	resources, derr := d.Discover(ctx, cfg.Selector)
	if derr != nil {
		detail.DiscoverErr = derr
		return detail, nil
	}
	rows := make([]ResourceRow, 0, len(resources))
	for _, r := range resources {
		row := ResourceRow{Resource: r, Status: sr.Statuses[r.ID()]}
		if describer, ok := describers[r.Type]; ok {
			if obs, err := describer.Describe(ctx, r.Ref); err != nil {
				row.LiveErr = err
			} else {
				row.Live = &obs
			}
		}
		rows = append(rows, row)
	}
	detail.Resources = rows
	return detail, nil
}

// sel をグループのセレクタとして書き込む
// グループが存在しない場合は mode=disabled で作成する
// 既存グループの Mode、Desired、cron 各種、Timezone は read-modify-write により保持し、変更はセレクタに限る
func SetSelector(ctx context.Context, s Store, group string, sel model.Selector) (created bool, err error) {
	if err := model.ValidGroupName(group); err != nil {
		return false, err
	}
	existing, err := s.GetGroup(ctx, group)
	if err != nil {
		return false, err
	}
	spec := model.NewGroupSpec(group)
	if existing != nil {
		spec = *existing
	}
	next, err := spec.WithSelector(sel)
	if err != nil {
		return false, err
	}
	if err := s.PutGroup(ctx, next); err != nil {
		return false, err
	}
	return existing == nil, nil
}

// グループを削除する
// 削除順は override、グループ単位のステータス、グループアイテム本体である
// この順序では、途中で失敗してもグループアイテムが残り、再試行のためにグループへ到達できる
// セレクタが一致していたリソースのステータスアイテムは残す
// 削除には動的な Discover が必要であり、かつ孤立した監査記録は他の動作に影響しないためである
// 手作業による削除手順は operations.md に記載がある
//
// 呼び出し側でグループ名を検証済みの場合も、本層のすべての入口が同じ検証を行う
// 検証を行う関数と行わない関数が混在すると、検証済みという前提が呼び出し側の実装に依存する
func RemoveGroup(ctx context.Context, s Store, group string) error {
	if err := model.ValidGroupName(group); err != nil {
		return err
	}
	if err := s.DeleteOverride(ctx, group); err != nil {
		return err
	}
	if err := s.DeleteGroupStatus(ctx, group); err != nil {
		return err
	}
	return s.DeleteGroup(ctx, group)
}

// 既存グループに対し、指定の desired state で mode=pinned を設定する
func Pin(ctx context.Context, s Store, group string, desired model.DesiredState) error {
	existing, err := requireGroup(ctx, s, group)
	if err != nil {
		return err
	}
	next, err := existing.Pin(desired)
	if err != nil {
		return err
	}
	return s.PutGroup(ctx, next)
}

// mode=pinned を解除し、書き込んだアイテムを返す
func Unpin(ctx context.Context, s Store, group string) (model.GroupSpec, error) {
	existing, err := requireGroup(ctx, s, group)
	if err != nil {
		return model.GroupSpec{}, err
	}
	next, err := existing.Unpin()
	if err != nil {
		return model.GroupSpec{}, err
	}
	if err := s.PutGroup(ctx, next); err != nil {
		return model.GroupSpec{}, err
	}
	return next, nil
}

// 既存グループに指定の cron で mode=schedule を設定し、書き込んだアイテムを返す
func Schedule(ctx context.Context, s Store, group string, spec model.ScheduleSpec) (model.GroupSpec, error) {
	existing, err := requireGroup(ctx, s, group)
	if err != nil {
		return model.GroupSpec{}, err
	}
	next, err := existing.WithSchedule(spec)
	if err != nil {
		return model.GroupSpec{}, err
	}
	if err := s.PutGroup(ctx, next); err != nil {
		return model.GroupSpec{}, err
	}
	return next, nil
}

// 他のグループ設定はそのままに mode=disabled を設定する
func Disable(ctx context.Context, s Store, group string) error {
	existing, err := requireGroup(ctx, s, group)
	if err != nil {
		return err
	}
	return s.PutGroup(ctx, existing.Disabled())
}

// グループに期限付きの override を書き込み、その失効時刻を返す
func SetOverride(ctx context.Context, s Store, group string, desired model.DesiredState, d time.Duration, now time.Time) (time.Time, error) {
	if err := desired.Validate(); err != nil {
		return time.Time{}, err
	}
	if d <= 0 {
		return time.Time{}, fmt.Errorf("override duration must be positive")
	}
	existing, err := requireGroup(ctx, s, group)
	if err != nil {
		return time.Time{}, err
	}
	// disabled は override より優先度の高い停止である
	// reconciler は override の評価前に disabled のグループをスキップするため、ここで受け付けても効果を持たない
	if existing.EffectiveMode() == model.ModeDisabled {
		return time.Time{}, fmt.Errorf("group %q is disabled; disabled overrides mode=schedule/pinned but is itself not overridable (schedule or pin it first)", group)
	}
	expiresAt := now.Add(d)
	if err := s.PutOverride(ctx, group, model.Override{Desired: desired, ExpiresAt: expiresAt.Unix()}); err != nil {
		return time.Time{}, err
	}
	return expiresAt, nil
}

// TTL の失効を待たずに override アイテムを削除する
// RemoveGroup と同じ理由により、ここでも名前を検証する
func ClearOverride(ctx context.Context, s Store, group string) error {
	if err := model.ValidGroupName(group); err != nil {
		return err
	}
	return s.DeleteOverride(ctx, group)
}

// 既存グループを取得して検証する
// Pin/Schedule/Disable/SetOverride が、名前の誤りによりグループを新規作成してはならないためである
// 初回利用時の作成を意図する SetSelector とは、この点で異なる
func requireGroup(ctx context.Context, s Store, group string) (*model.GroupSpec, error) {
	if err := model.ValidGroupName(group); err != nil {
		return nil, err
	}
	existing, err := s.GetGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, fmt.Errorf("group %q not found (create it with: cheapskate-cli set-selector --group %s ...)", group, group)
	}
	return existing, nil
}
