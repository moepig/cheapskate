package model

import (
	"fmt"
	"regexp"
	"time"

	"github.com/adhocore/gronx"
)

// グループが管理対象のリソースをどう扱うかを表す
type Mode string

const (
	ModePinned   Mode = "pinned"
	ModeSchedule Mode = "schedule"
	ModeDisabled Mode = "disabled"
)

var groupNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// name がターゲットグループ名として使用可能かを報告する
// pk の区切り文字である "#" と "/" を除外し、URL および SNS の Subject として安全な範囲に限定する
func ValidGroupName(name string) error {
	if !groupNameRE.MatchString(name) {
		return fmt.Errorf("invalid group name %q (must match %s)", name, groupNameRE.String())
	}
	return nil
}

// グループの設定より優先される、期限付きの desired state
// TTL 付きで保存するため自動的に失効する
// TTL による削除は遅延するため、読み出し時にも ExpiresAt を検査する
type Override struct {
	Desired   DesiredState
	ExpiresAt int64 // epoch 秒
}

// 保存されたままの未検証のターゲットグループ設定
// 設定が壊れたグループも CLI と web console が表示・修正できるよう、検証を行わない
// reconciler が従う検証済みの GroupConfig を得るには、ParseGroup を通す
// reconciler がこれを書き込むことはない
//
// 設定の変更は、この型のメソッド (Pin、Unpin、WithSchedule、Disabled、WithSelector) として実装する
// いずれの遷移も結果を ParseGroup へ通してから返すため、保存されるのは reconciler が従える設定に限られる
// 設定の妥当性の定義と設定の変更手段を分離した場合、アプリケーション層が ParseGroup に拒否される設定を書き込める (Disabled のみ例外であり、その理由は当該箇所に記す)
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

// 未設定のグループの spec を返す
// mode を明示的に disabled とするのは、ParseGroup の既定に依存せず、保存アイテムにも記録するためである
func NewGroupSpec(name string) GroupSpec {
	return GroupSpec{Name: name, Mode: ModeDisabled}
}

// このグループのセレクタを返す (未設定の場合はゼロ値)
func (g GroupSpec) Selector() Selector {
	return Selector{TagKey: g.TagKey, TagValue: g.TagValue, Types: normalizeTypes(g.Types)}
}

// reconciler が従う mode を返す
// 未設定を disabled とみなす既定は ParseGroup と同じである
// Mode を直接比較した場合、mode 属性を持たないアイテムに対して、reconciler は disabled と判定し、設定側は disabled ではないと判定するため、両者の判定が食い違う
func (g GroupSpec) EffectiveMode() Mode {
	if g.Mode == "" {
		return ModeDisabled
	}
	return g.Mode
}

// 遷移の結果が ParseGroup を通ることを確かめ、通る場合は spec をそのまま返す
// 変更操作がこれを通ることにより、保存済みの設定に reconciler が従えるという不変条件を書き込み側で保証する
// GroupConfig ではなく GroupSpec を返すのは、保存形状が GroupSpec であり、設定が壊れたグループを修復する経路も同じ型を扱うためである
func (g GroupSpec) validated() (GroupSpec, error) {
	if _, err := ParseGroup(g); err != nil {
		return GroupSpec{}, err
	}
	return g, nil
}

// mode=pinned を desired で設定した spec を返す
// cron 系のフィールドは保持する
// mode=pinned では作用せず、Unpin と WithSchedule により復帰できるためである
func (g GroupSpec) Pin(desired DesiredState) (GroupSpec, error) {
	if err := desired.Validate(); err != nil {
		return GroupSpec{}, err
	}
	out := g
	out.Mode, out.Desired = ModePinned, desired
	return out.validated()
}

// mode=pinned を解除した spec を返す
// cron が残っている場合は、それを用いて mode=schedule へ戻す (Pin は cron を削除しない)
// スケジュールを設定したことがなく復帰先が存在しない場合は、mode=disabled とする
//
// schedule へ戻す場合、保存されている cron の妥当性をここで検証する
// 不正な cron のまま mode=schedule とした場合、操作は成功として報告される一方、reconciler は毎サイクル同じ設定エラーを出力する
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
// Desired は保持しない
// mode=schedule では cron が desired state を決定するため、pin の設定値は参照されないためである
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
// 他の設定はすべて保持する
//
// 遷移のうち、結果を検証しないのはこれのみである
// disabled は reconciler が何も行わないことを意味するため、他の設定の内容によらず影響を持たない
// また、設定が壊れているグループの管理を停止する唯一の手段であるため、その設定の破損を理由に失敗してはならない
func (g GroupSpec) Disabled() GroupSpec {
	out := g
	out.Mode = ModeDisabled
	return out
}

// セレクタを差し替えた spec を返す
// mode と cron を含む他の設定は保持する
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
	Name      string
	Mode      Mode
	Desired   DesiredState
	StartCron string
	StopCron  string
	Timezone  string
	Selector  Selector
}

// 保存されたグループ設定を検証する
// セレクタを持たない状態でもグループを作成できる (disabled で作成し、WithSelector で設定する)
// ただし、有効なセレクタなしに mode を pinned または schedule へ変更することは、設定エラーとして扱う
// mode=pinned が有効な Desired を必須とする規則と対応する
//
// mode=schedule の場合は、cron と timezone もここで検証する
// reconciler が従えない設定の判定を、この 1 か所へ集約するためである
// 集約しない場合、cron の妥当性を判定できるのは書き込み側の経路のみとなり、手作業または旧バージョンにより保存された不正な cron を doctor が config-error として報告できない
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
// 保存済みの設定を読む ParseGroup と、新しい設定を書く GroupSpec.WithSchedule が共有する
// 空の timezone は不正ではなく、reconciler の既定タイムゾーンを用いることを表す
func validateSchedule(startCron, stopCron, timezone string) error {
	if startCron == "" && stopCron == "" {
		return fmt.Errorf("mode=schedule requires a start and/or stop cron")
	}
	// 両方が不正な場合の報告対象を決定的とするため、スライスの順に検査する
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
