// override > pinned > cron の優先順位で desired state を解決する
package schedule

import (
	"fmt"
	"time"

	"github.com/adhocore/gronx"

	"cheapskate/internal/core/model"
)

// DesiredRunning または DesiredStopped を返す
// グループが disabled の場合は DesiredNone を返す
func ResolveDesired(group model.GroupConfig, override *model.Override, now time.Time, defaultTimezone string) (model.DesiredState, error) {
	if group.Mode == model.ModeDisabled {
		return model.DesiredNone, nil
	}
	if override != nil && override.ExpiresAt > now.Unix() {
		return override.Desired, nil
	}
	if group.Mode == model.ModePinned {
		return group.Desired, nil
	}
	return fromSchedule(group, now, defaultTimezone)
}

// cron から desired state を導出する
// model.ParseGroup が mode=schedule の cron と timezone を検証済みであるため、ここで残る失敗は既定タイムゾーンが不正な場合に限る
// 既定タイムゾーンは reconciler の環境変数に由来し、グループ設定には含まれない
// それ以外の検査は、GroupConfig を直接組み立てた呼び出しに対する防御として残す
//
// cron はグループのタイムゾーンのローカル壁時計時刻で解釈するため、夏時間の切り替え日には次の 2 つが生じる
// いずれも schedule_test.go が仕様として固定している
//   - 春の繰り上げ: 存在しない時刻に置いた cron は、その日は発火しない
//     start が発火しない場合、そのグループはその日は停止したままとなる
//     stop が発火しない場合、そのグループは次の stop まで起動したままとなる
//   - 秋の繰り下げ: 2 回訪れる時刻に置いた cron は、その日 2 回発火しうる
//     desired state は直近に発火した側のみで決まる冪等な導出であるため、2 回目でも結果は変わらない
//
// したがって影響が生じるのは春の繰り上げのみである
// 夏時間のある地域では、切り替えの時間帯を避けた時刻へ cron を置くこと
func fromSchedule(group model.GroupConfig, now time.Time, defaultTimezone string) (model.DesiredState, error) {
	tzName := group.Timezone
	if tzName == "" {
		tzName = defaultTimezone
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return model.DesiredNone, fmt.Errorf("group %s: invalid timezone %q: %w", group.Name, tzName, err)
	}
	localNow := now.In(loc)

	lastStart, err := prev(group.StartCron, localNow)
	if err != nil {
		return model.DesiredNone, fmt.Errorf("group %s: start_cron: %w", group.Name, err)
	}
	lastStop, err := prev(group.StopCron, localNow)
	if err != nil {
		return model.DesiredNone, fmt.Errorf("group %s: stop_cron: %w", group.Name, err)
	}

	switch {
	case lastStart == nil && lastStop == nil:
		return model.DesiredNone, fmt.Errorf("group %s: mode=schedule requires start_cron and/or stop_cron", group.Name)
	case lastStart == nil:
		return model.DesiredStopped, nil
	case lastStop == nil:
		return model.DesiredRunning, nil
	}
	// 直近に発火した側の cron が desired state を決定する。同時刻の場合は stop とする
	if lastStart.After(*lastStop) {
		return model.DesiredRunning, nil
	}
	return model.DesiredStopped, nil
}

func prev(expr string, localNow time.Time) (*time.Time, error) {
	if expr == "" {
		return nil, nil
	}
	t, err := gronx.PrevTickBefore(expr, localNow, true)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
