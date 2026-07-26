package model

import (
	"fmt"
	"regexp"
	"time"

	"github.com/adhocore/gronx"
)

// グループが管理下のリソースをどう扱うかの方針
type Mode string

const (
	ModePinned   Mode = "pinned"
	ModeSchedule Mode = "schedule"
	ModeDisabled Mode = "disabled"
)

var groupNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// name がターゲットグループ名として使えるかを報告する
// pk の区切り文字である "#" と "/" を除外し、URL および SNS の Subject として安全な範囲に収める
func ValidGroupName(name string) error {
	if !groupNameRE.MatchString(name) {
		return fmt.Errorf("invalid group name %q (must match %s)", name, groupNameRE.String())
	}
	return nil
}

// グループの設定より優先される、期限付きの desired state
// TTL 付きで保存されるため自動的に失効する
// ただし TTL による削除は遅延するので、読み出し時にも ExpiresAt を検査する
type Override struct {
	Desired   DesiredState
	ExpiresAt int64 // epoch 秒
}

// オペレータが保存したままのターゲットグループ設定であり、未検証のもの
// 設定が壊れたグループでも CLI や web console が表示・修正できるよう、あえて検証しない
// reconciler が実際に従う検証済みの GroupConfig を得るには ParseGroup に通す
// reconciler 自身がこれを書き込むことはない
//
// 設定変更はこの型のメソッド（Pin・Unpin・WithSchedule・Disabled・WithSelector）として実装してある
// どの遷移も結果を ParseGroup へ通してから返すので、保存されるのは reconciler が従える設定だけになる
// 「どんな設定が妥当か」と「どう設定を変えるか」が離れていると、アプリケーション層が後から ParseGroup に拒否される設定を書けてしまう（Disabled だけは例外で、その理由はそこに書いた）
type GroupSpec struct {
	Name      string
	Mode      Mode
	Desired   DesiredState
	StartCron string
	StopCron  string
	Timezone  string
	TagKey    string
	TagValue  string
	Types     []ResourceType
}

// まだ何も設定されていないグループの spec を返す
// mode を明示的に disabled にしているのは、ParseGroup の既定に頼らず保存アイテムにもそう書き残すためである
func NewGroupSpec(name string) GroupSpec {
	return GroupSpec{Name: name, Mode: ModeDisabled}
}

// このグループのセレクタを取り出す（未設定ならゼロ値）
func (g GroupSpec) Selector() Selector {
	return Selector{TagKey: g.TagKey, TagValue: g.TagValue, Types: normalizeTypes(g.Types)}
}

// reconciler が実際に従う mode を返す
// 未設定を disabled とみなす既定は ParseGroup と同じである
// 生の Mode と比べてしまうと、mode 属性のないアイテム（手で作られたもの）に対して reconciler は「disabled だから何もしない」と判断するのに、設定側は「disabled ではない」と判断する、という食い違いが起きる
func (g GroupSpec) EffectiveMode() Mode {
	if g.Mode == "" {
		return ModeDisabled
	}
	return g.Mode
}

// 遷移の結果が ParseGroup を通ることを確かめ、通れば spec をそのまま返す
// 変更操作がこれを通ることが、「保存済みの設定は reconciler が従える」という不変条件の書き込み側の担保である
// 返すのが GroupConfig ではなく GroupSpec なのは、保存形状がこちらであり、また設定が壊れたグループを修復する経路も同じ型を扱う必要があるためである
func (g GroupSpec) validated() (GroupSpec, error) {
	if _, err := ParseGroup(g); err != nil {
		return GroupSpec{}, err
	}
	return g, nil
}

// mode=pinned を desired で設定した spec を返す
// cron 系のフィールドは残す
// mode=pinned では作用せず、Unpin や WithSchedule で復帰させられるためである
func (g GroupSpec) Pin(desired DesiredState) (GroupSpec, error) {
	if err := desired.Validate(); err != nil {
		return GroupSpec{}, err
	}
	out := g
	out.Mode, out.Desired = ModePinned, desired
	return out.validated()
}

// mode=pinned を解除した spec を返す
// cron が残っていればそれを使って mode=schedule へ戻す（Pin は cron を消さない）
// 一度もスケジュール設定されておらず戻す先がない場合は mode=disabled にする
//
// schedule へ戻す場合は、保存されている cron が本当に使えるかをここで確かめる
// 使えない cron のまま mode=schedule にすると、reconciler が 5 分ごとに同じ設定エラーを出し続けるだけの状態へ、成功したように見える操作で入ってしまう
func (g GroupSpec) Unpin() (GroupSpec, error) {
	if g.StartCron == "" && g.StopCron == "" {
		return g.Disabled(), nil
	}
	out := g
	out.Mode = ModeSchedule
	return out.validated()
}

// Schedule の cron 設定
type ScheduleSpec struct {
	StartCron string
	StopCron  string
	Timezone  string
}

// 指定の cron で mode=schedule を設定した spec を返す
// Desired は残さない
// mode=schedule では cron が desired state を決めるので、古い pin の値が残っていると読み手を惑わせるだけである
func (g GroupSpec) WithSchedule(spec ScheduleSpec) (GroupSpec, error) {
	if err := validateSchedule(spec.StartCron, spec.StopCron, spec.Timezone); err != nil {
		return GroupSpec{}, err
	}
	out := g
	out.Mode, out.Desired = ModeSchedule, DesiredNone
	out.StartCron, out.StopCron, out.Timezone = spec.StartCron, spec.StopCron, spec.Timezone
	return out.validated()
}

// mode=disabled を設定した spec を返す
// 他の設定はすべてそのまま残す
//
// 遷移のなかでこれだけは結果を検証しない
// disabled は「reconciler が何もしない」を意味するので、どんな設定と組み合わせても危険がない
// そして設定が壊れているグループの管理を今すぐ止めるための最後の手段でもあるから、まさにその壊れた設定を理由に失敗してはならない
func (g GroupSpec) Disabled() GroupSpec {
	out := g
	out.Mode = ModeDisabled
	return out
}

// セレクタを差し替えた spec を返す
// mode や cron といった他の設定は保つ
func (g GroupSpec) WithSelector(sel Selector) (GroupSpec, error) {
	if err := sel.Validate(); err != nil {
		return GroupSpec{}, err
	}
	out := g
	out.TagKey, out.TagValue, out.Types = sel.TagKey, sel.TagValue, normalizeTypes(sel.Types)
	return out.validated()
}

// 検証済みの GroupSpec
type GroupConfig struct {
	Name      string // 例: "dev"
	Mode      Mode
	Desired   DesiredState
	StartCron string
	StopCron  string
	Timezone  string
	Selector  Selector
}

// 保存されたグループ設定を検証する
// セレクタが一切ない状態でもグループは作成できる（disabled で始まり、WithSelector で設定される）
// ただし有効なセレクタなしに mode を pinned や schedule へ変えるのは設定エラーとして扱う
// これは mode=pinned が有効な Desired を必須とする既存ルールと対応している
//
// mode=schedule のときは cron と timezone もここで検証する
// 「reconciler が従えない設定」の判定をこの 1 か所に集めるためである
// これがないと、cron の妥当性を知っているのが書き込み側の経路だけになり、手で編集された（あるいは旧バージョンが残した）不正な cron を doctor が config-error として報告できない
func ParseGroup(item GroupSpec) (GroupConfig, error) {
	name := item.Name
	if err := ValidGroupName(name); err != nil {
		return GroupConfig{}, err
	}
	mode := item.EffectiveMode()
	switch mode {
	case ModePinned, ModeSchedule, ModeDisabled:
	default:
		return GroupConfig{}, fmt.Errorf("group %s: unknown mode %q", name, mode)
	}
	if mode == ModePinned && item.Desired.Validate() != nil {
		return GroupConfig{}, fmt.Errorf("group %s: mode=pinned requires desired running|stopped", name)
	}
	sel := item.Selector()
	if mode != ModeDisabled {
		if sel.Empty() {
			return GroupConfig{}, fmt.Errorf("group %s: mode=%s requires a selector (set one first)", name, mode)
		}
		if err := sel.Validate(); err != nil {
			return GroupConfig{}, fmt.Errorf("group %s: %w", name, err)
		}
	} else if !sel.Empty() {
		if err := sel.Validate(); err != nil {
			return GroupConfig{}, fmt.Errorf("group %s: %w", name, err)
		}
	}
	if mode == ModeSchedule {
		if err := validateSchedule(item.StartCron, item.StopCron, item.Timezone); err != nil {
			return GroupConfig{}, fmt.Errorf("group %s: %w", name, err)
		}
	}
	return GroupConfig{
		Name:      name,
		Mode:      mode,
		Desired:   item.Desired,
		StartCron: item.StartCron,
		StopCron:  item.StopCron,
		Timezone:  item.Timezone,
		Selector:  sel,
	}, nil
}

// mode=schedule のグループが従えるスケジュール設定かを検査する
// ParseGroup（保存済みの設定を読むとき）と GroupSpec.WithSchedule（新しい設定を書くとき）が共有する
// 空の timezone は不正ではなく、reconciler の既定タイムゾーンを使うことを意味する
func validateSchedule(startCron, stopCron, timezone string) error {
	if startCron == "" && stopCron == "" {
		return fmt.Errorf("mode=schedule requires a start and/or stop cron")
	}
	// 両方とも不正な場合にどちらを報告するかを決定的にするため、スライスで順に見る
	for _, c := range []struct {
		label string
		expr  string
	}{{"start_cron", startCron}, {"stop_cron", stopCron}} {
		if c.expr != "" && !gronx.IsValid(c.expr) {
			return fmt.Errorf("invalid %s expression %q", c.label, c.expr)
		}
	}
	if timezone != "" {
		if _, err := time.LoadLocation(timezone); err != nil {
			return fmt.Errorf("invalid timezone %q", timezone)
		}
	}
	return nil
}
