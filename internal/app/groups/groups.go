// cheapskate-cli と web console が共有する設定操作を実装する
// どちらのフロントエンドとも同じく、触れるのは DynamoDB のアイテムと読み取り専用の tag:GetResources API だけである
// RDS/ECS/EC2 のコントロール API には決して触れない
//
// 設定をどう変えるかの規則そのものは持たない
// 「pin は cron を残す」「unpin は cron があれば schedule へ戻る」といった遷移は model.GroupSpec のメソッドにあり、ここはその前後の読み書きを担う
// 遷移側が結果を検証してから返すので、この層が model.ParseGroup に後から拒否される設定を保存することはない
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
// *state.Store が満たすが、設定操作が受け取るのはこの窓口だけである
//
// UpdateStatus を意図的に含めていない
// status# アイテムを所有するのは reconciler であり、CLI と web console にとっては読み取り専用の表示材料である
// 型に書いておけば、設定操作から監査証跡を書き換える経路が存在しなくなる
type Store interface {
	ScanAll(ctx context.Context, now time.Time) (state.ScanResult, error)
	GetGroup(ctx context.Context, name string) (*model.GroupSpec, error)
	PutGroup(ctx context.Context, spec model.GroupSpec) error
	PutOverride(ctx context.Context, group string, o model.Override) error
	DeleteGroup(ctx context.Context, name string) error
	DeleteOverride(ctx context.Context, name string) error
	DeleteGroupStatus(ctx context.Context, name string) error
}

// ターゲットグループ 1 件を、その override とグループ単位のステータスとともに表したもの
// 1 回の ScanAll から組み立て、リソースの動的探索は行わない
// List は高速な Scan 1 回に保つ必要があり、探索は GetDetail で 1 グループずつ行う
// このグループの override、group#、group-status のいずれかが壊れていた場合は Err が設定される
// その場合も行自体は表示し、一覧全体が消えるのではなくオペレータが見て直せるようにする
type GroupRow struct {
	Name     string
	Group    model.GroupSpec
	Override *model.Override
	Status   model.Status
	Err      error
}

// 登録済みの全グループを、override とグループ単位のステータスを解決した状態で返す
// グループごとに GetItem を投げるのではなく、Scan 1 回（state.ScanAll）で済ませる
// override やステータスはあるのに group# アイテムがない名前は孤立データとみなし、ここには載せない
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

// グループのセレクタに現在マッチしているリソース 1 件を、そのステータスと結合したもの
// 種別に対応する port.Describer が結線されていれば、現在の状態のライブなスナップショットも併せ持つ
// 種別に対応する Describer がない場合や、ライブの Describe 呼び出しが失敗した場合、Live は nil になる（LiveErr を参照）
// いずれの場合も「不明」に劣化するだけで、行全体のエラーにはしない
type ResourceRow struct {
	Resource model.Resource
	Status   model.Status
	Live     *model.Observation
	LiveErr  error
}

// グループの完全な詳細で、設定・override・グループ単位のステータスを含む
// セレクタが設定されていれば、現在マッチする全リソースを動的に探索し、リソース単位のステータスと結合して併せ持つ
// Discover の失敗はエラーではなくデータとして DiscoverErr に載せて返す
// これにより tag:GetResources の権限不足や不備は、ページやコマンド全体の失敗ではなく画面内のメッセージに留まる
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
// セレクタがあればメンバーを動的に探索し、メンバーごとに describers[member.Type] があれば現在の状態を問い合わせる
// マップが nil や空でも問題なく、その場合は各行の Live が nil のままになるだけである
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
// グループが存在しなければ mode=disabled で作成する
// これは旧来のメンバー方式の Add が持っていた初回作成の挙動を踏襲したものだが、対象はリソース 1 件の登録ではなくグループ所属の定義である
// 既存グループの Mode・Desired・cron 各種・Timezone は read-modify-write で保たれ、変わるのはセレクタだけである
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

// グループを完全に削除する
// 削除順は override、グループ単位のステータス、グループアイテム本体である
// この順なら途中で失敗してもグループアイテムが残り、再試行のためにグループへ到達できる
// セレクタがマッチしていた個々のリソースのステータスアイテムは残す
// 消すには動的な Discover が必要になるうえ、無害な孤立した監査記録に過ぎないためである
// 手作業で片づけたい場合の手順は operations.md に記載がある
//
// 呼び出し側でグループ名がすでに塞がれていても、この層のどの入口も同じ検証を通す
// 名前の検証を入口ごとに省ける関数とそうでない関数が混ざると、「ここは検証済みのはず」という前提が呼び出し側の都合に依存してしまう
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
	// disabled は override より強い停止である
	// reconciler は override を見る前に disabled のグループをスキップするため、ここで受け付けても黙って何も起きない
	if existing.EffectiveMode() == model.ModeDisabled {
		return time.Time{}, fmt.Errorf("group %q is disabled; disabled overrides mode=schedule/pinned but is itself not overridable (schedule or pin it first)", group)
	}
	expiresAt := now.Add(d)
	if err := s.PutOverride(ctx, group, model.Override{Desired: desired, ExpiresAt: expiresAt.Unix()}); err != nil {
		return time.Time{}, err
	}
	return expiresAt, nil
}

// TTL を待たずに override アイテムを今すぐ削除する
// RemoveGroup と同じ理由で、ここでも名前を検証する
func ClearOverride(ctx context.Context, s Store, group string) error {
	if err := model.ValidGroupName(group); err != nil {
		return err
	}
	return s.DeleteOverride(ctx, group)
}

// 既存グループを取得して検証する
// Pin/Schedule/Disable/SetOverride が打ち間違いから黙ってグループを作ってしまってはならないためである
// 初回利用時に作成するのが設計上の意図である SetSelector とは、この点が異なる
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
