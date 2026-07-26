// state テーブルの不整合と、中途半端に終わった処理が残したゴミデータを診断する
// cheapskate-cli の `doctor` と web console の /doctor が共有する
//
// 既定では読み取りだけを行う
// Prune を立てたときに限って削除するが、消すのは「そのグループが存在しない」ことが Scan だけで確定する
// 孤立レコードと、どのグループのセレクタにも一致しないリソースのステータスに限る
// 設定エラー・壊れたレコード・セレクタ重複は人間が直すべき判断なので、報告するだけで触らない
//
// メンバーの所属は動的探索で決まるため、Discover が 1 つでも失敗したサイクルでは「どのグループにも属していない」を根拠にした削除を必ず見送る（Report.Blocked を参照）
// 一時的に見えていないだけのリソースの監査記録を消してしまわないためである
package doctor

import (
	"context"
	"fmt"
	"sort"
	"time"

	"cheapskate/internal/app/port"
	"cheapskate/internal/core/model"
	"cheapskate/internal/state"
)

// doctor が state テーブルに求める範囲
// *state.Store が満たすが、doctor が受け取るのはこの窓口だけである
//
// 書き込みは削除だけであり、しかも --prune が明示されたときにしか呼ばない
// グループ設定やステータスの内容を書き換えるメソッドは含めていない
// 診断が設定を「直して」しまわないことが、型として読み取れるようにするためである
type Store interface {
	ScanAll(ctx context.Context, now time.Time) (state.ScanResult, error)
	DeleteOverride(ctx context.Context, name string) error
	DeleteGroupStatus(ctx context.Context, name string) error
	DeleteStatus(ctx context.Context, resourceID string) error
}

// 検出項目の種類
type Kind string

const (
	KindOrphanOverride     Kind = "orphan-override"     // group# のない override#
	KindOrphanGroupStatus  Kind = "orphan-group-status" // group# のない status#group#
	KindOrphanStatus       Kind = "orphan-status"       // どのグループのセレクタにも一致しないリソースの status#
	KindCorruptRecord      Kind = "corrupt-record"      // unmarshal / 検証に失敗するレコード
	KindConfigError        Kind = "config-error"        // 登録済みだが reconciler が従えない設定
	KindDiscoverError      Kind = "discover-error"      // セレクタは妥当だが tag:GetResources が失敗した
	KindSelectorOverlap    Kind = "selector-overlap"    // 複数グループのセレクタが同じリソースを取り合っている
	KindStuckTransitioning Kind = "stuck-transitioning" // StuckAfter を超えて遷移中のまま
)

// 検出項目 1 件
type Finding struct {
	Kind     Kind   `json:"kind"`
	Group    string `json:"group,omitempty"`
	Resource string `json:"resource,omitempty"` // model.Resource.ID() 形式
	PK       string `json:"pk,omitempty"`       // 生の DynamoDB キー（手作業の delete-item 用）
	Detail   string `json:"detail"`
	Prunable bool   `json:"prunable"` // Kind から導出される（pruners を参照）ので、呼び出し側では設定しない
	Pruned   bool   `json:"pruned,omitempty"`
	PruneErr string `json:"prune_error,omitempty"`
}

// doctor 1 回分の結果
type Report struct {
	Findings []Finding `json:"findings"`
	// orphan-status の判定を見送った理由
	// 空でない場合、この Report に orphan-status が 1 件も載っていないことは「孤立レコードがない」ではなく「調べられなかった」を意味する
	Blocked []string `json:"blocked,omitempty"`
	Pruned  int      `json:"pruned"`
}

// 検出項目があるかどうかを報告する
func (r Report) Clean() bool { return len(r.Findings) == 0 }

type Options struct {
	Prune bool
	// これを超えて遷移中のままのリソースを stuck-transitioning として報告する
	// ゼロ値なら DefaultStuckAfter
	StuckAfter time.Duration
}

// 遷移中とみなし続ける既定の上限
// RDS の停止・起動は数分、ECS のドレインは既定 5 分ほどで終わる
// それを大きく超えても遷移中なら、待っていても解決しない何かが起きている可能性が高い
const DefaultStuckAfter = 30 * time.Minute

// 診断を 1 回実行し、opts.Prune が立っていれば削除まで行う
func Run(ctx context.Context, s Store, d port.Discoverer, now time.Time, opts Options) (Report, error) {
	if opts.StuckAfter <= 0 {
		opts.StuckAfter = DefaultStuckAfter
	}
	sr, err := s.ScanAll(ctx, now)
	if err != nil {
		return Report{}, err
	}

	var report Report
	// リソース ID -> そのリソースを今マッチしているグループ名（探索順、つまりグループ名順）
	owners := map[string][]string{}

	for _, row := range sr.Groups {
		report.inspectGroup(ctx, d, row, owners)
	}

	report.checkOverlaps(owners)
	report.checkOrphanStatuses(sr.Statuses, owners)
	report.checkStuck(sr.Statuses, owners, now, opts.StuckAfter)

	sort.SliceStable(report.Findings, func(i, j int) bool {
		a, b := report.Findings[i], report.Findings[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Resource < b.Resource
	})

	if opts.Prune {
		report.prune(ctx, s)
	}
	return report, nil
}

// 種別ごとの削除方法
// このマップに載っていることが「削除してよい」の唯一の定義であり、Finding.Prunable もここから決まる（add を参照）
// 新しい種別を削除対象にするには、ここへ削除方法を書く以外に方法がない
// 「削除してよいか」と「どう削除するか」を別々に書くと、片方だけ足したときに削除対象のはずの項目が黙って残り、しかも読み取り専用実行との区別がつかなくなる
var pruners = map[Kind]func(context.Context, Store, Finding) error{
	KindOrphanOverride:    func(ctx context.Context, s Store, f Finding) error { return s.DeleteOverride(ctx, f.Group) },
	KindOrphanGroupStatus: func(ctx context.Context, s Store, f Finding) error { return s.DeleteGroupStatus(ctx, f.Group) },
	KindOrphanStatus:      func(ctx context.Context, s Store, f Finding) error { return s.DeleteStatus(ctx, f.Resource) },
}

func (r *Report) add(f Finding) {
	_, f.Prunable = pruners[f.Kind]
	r.Findings = append(r.Findings, f)
}

// orphan-status の判定を見送る理由を記録する
func (r *Report) block(format string, args ...any) {
	r.Blocked = append(r.Blocked, fmt.Sprintf(format, args...))
}

// グループ行 1 件を診断し、そのセレクタにマッチするリソースを owners へ積む
func (r *Report) inspectGroup(ctx context.Context, d port.Discoverer, row state.GroupRow, owners map[string][]string) {
	if row.Err != nil {
		r.add(Finding{Kind: KindCorruptRecord, Group: row.Name, Detail: row.Err.Error()})
		// 壊れているのが group# 自身なら、そのセレクタが何にマッチするか分からない
		r.block("group %q has a corrupt record; its members could not be enumerated", row.Name)
		if !row.HasGroup {
			// HasGroup は「group# を読めた」であって「group# がない」ではない
			// 読めなかったレコードがその group# 自身だったなら、アイテムは存在するのにここでは見えていない
			// それを「グループが存在しない」の根拠にすると、生きている override や group-status を孤立レコードとして消してしまう（しかも同じレポートが同時に corrupt-record を主張する）
			// どちらなのかは Scan だけでは決まらないので、孤立判定ごと見送って人間に回す
			return
		}
	}
	if !row.HasGroup {
		// override や group-status だけが残っている
		// グループが消えた（あるいは一度も作られなかった）ことが Scan だけで確定するので、これは安全に消せる
		if row.Override != nil {
			r.add(Finding{
				Kind: KindOrphanOverride, Group: row.Name, PK: state.OverridePK(row.Name),
				Detail: fmt.Sprintf("override desired=%s expires_at=%s but group %q is not registered", row.Override.Desired, time.Unix(row.Override.ExpiresAt, 0).UTC().Format(time.RFC3339), row.Name),
			})
		}
		if row.Status != (model.Status{}) {
			r.add(Finding{
				Kind: KindOrphanGroupStatus, Group: row.Name, PK: state.GroupStatusPK(row.Name),
				Detail: fmt.Sprintf("group-level status record left behind; group %q is not registered", row.Name),
			})
		}
		return
	}

	if _, err := model.ParseGroup(row.Group); err != nil {
		// reconciler はこのグループに従えない
		// セレクタ自体は妥当かもしれないので、探索は下で別途試す
		r.add(Finding{Kind: KindConfigError, Group: row.Name, PK: state.GroupPK(row.Name), Detail: err.Error()})
	}

	sel := row.Group.Selector()
	if sel.Empty() {
		return // セレクタ未設定のグループはメンバーを持たない
	}
	if err := sel.Validate(); err != nil {
		r.block("group %q has an invalid selector (%v); its members could not be enumerated", row.Name, err)
		return
	}
	resources, err := d.Discover(ctx, sel)
	if err != nil {
		r.add(Finding{Kind: KindDiscoverError, Group: row.Name, Detail: err.Error()})
		r.block("group %q could not be discovered (%v); its members are unknown", row.Name, err)
		return
	}
	for _, res := range resources {
		id := res.ID()
		owners[id] = append(owners[id], row.Name)
	}
}

// 同じリソースを 2 つ以上のグループが取り合っている状態を報告する
// reconciler ではグループ名順で最初のグループが所有し、あとに来たグループは自分の status#group# にエラーを記録する（reconcile.ReconcileGroup を参照）
// つまり片方の設定は黙って無視されているので、これは必ず人間が直すべき不整合である
func (r *Report) checkOverlaps(owners map[string][]string) {
	for id, groups := range owners {
		if len(groups) < 2 {
			continue
		}
		r.add(Finding{
			Kind: KindSelectorOverlap, Group: groups[0], Resource: id,
			Detail: fmt.Sprintf("matched by %d groups %v; only %q takes effect, the rest are ignored", len(groups), groups, groups[0]),
		})
	}
}

// どのグループのセレクタにも一致しないリソースの status# を報告する
// タグを外した、リソースを消した、グループを消した、のいずれかで取り残された監査記録である
// 探索が 1 つでも欠けているサイクルでは判定そのものを行わない
func (r *Report) checkOrphanStatuses(statuses map[string]model.Status, owners map[string][]string) {
	if len(r.Blocked) > 0 {
		return
	}
	for id := range statuses {
		if len(owners[id]) > 0 {
			continue
		}
		r.add(Finding{
			Kind: KindOrphanStatus, Resource: id, PK: state.StatusPK(id),
			Detail: fmt.Sprintf("status record for %s, which matches no group's selector", id),
		})
	}
}

// StuckAfter を超えて遷移中のままのリソースを報告する
// reconciler は遷移中のリソースを毎サイクル黙って skip するので、これが唯一の手がかりになる
// 解析できない transitioning_since は壊れたレコードとして扱う
// 消し忘れではなく書き込み側の不具合を示すためである
func (r *Report) checkStuck(statuses map[string]model.Status, owners map[string][]string, now time.Time, stuckAfter time.Duration) {
	for id, st := range statuses {
		if st.TransitioningSince == "" {
			continue
		}
		since, err := time.Parse(time.RFC3339, st.TransitioningSince)
		if err != nil {
			r.add(Finding{
				Kind: KindCorruptRecord, Resource: id, PK: state.StatusPK(id),
				Detail: fmt.Sprintf("transitioning_since %q is not RFC3339: %v", st.TransitioningSince, err),
			})
			continue
		}
		elapsed := now.Sub(since)
		if elapsed <= stuckAfter {
			continue
		}
		f := Finding{
			Kind: KindStuckTransitioning, Resource: id, PK: state.StatusPK(id),
			Detail: fmt.Sprintf("transitioning for %s (since %s); the reconciler skips it every cycle", elapsed.Round(time.Minute), st.TransitioningSince),
		}
		if g := owners[id]; len(g) > 0 {
			f.Group = g[0]
		}
		r.add(f)
	}
}

// Prunable な検出項目を削除する
// 1 件ごとに結果を記録し、途中で失敗しても残りの削除は続ける
// 削除は冪等（存在しないアイテムの DeleteItem はエラーにならない）なので、失敗したら doctor をもう一度流せばよい
func (r *Report) prune(ctx context.Context, s Store) {
	for i := range r.Findings {
		f := &r.Findings[i]
		del, ok := pruners[f.Kind]
		if !ok {
			continue // 設定エラー・セレクタ重複・壊れたレコードなど、人間が直すべき項目
		}
		if err := del(ctx, s, *f); err != nil {
			f.PruneErr = err.Error()
			continue
		}
		f.Pruned = true
		r.Pruned++
	}
}
