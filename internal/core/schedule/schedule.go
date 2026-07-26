// override > pinned > cron の優先順位で desired state を解決する
package schedule

import (
	"fmt"
	"time"

	"github.com/adhocore/gronx"

	"cheapskate/internal/core/model"
)

// DesiredRunning または DesiredStopped を返す
// グループが disabled のときは DesiredNone を返す
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

// cron から desired state を導く
// model.ParseGroup が mode=schedule の cron と timezone をすでに検証しているので、ここで残る失敗は既定タイムゾーン（reconciler の環境変数由来で、グループ設定の一部ではない）が不正な場合だけである
// それ以外の検査は、GroupConfig を直接組み立てた呼び出しに対する防御として残してある
//
// cron はグループのタイムゾーンのローカル壁時計時刻で解釈するため、夏時間の切り替え日には次の 2 つが起きる
// （どちらも schedule_test.go で仕様として固定してある）
//   - 春の繰り上げ：消える時刻（America/New_York なら 02:00-02:59）に置いた cron は、その日まるごと発火しない
//     start が飛べばそのグループはその日ずっと停止したまま（安価側）だが、stop が飛べば次の stop まで動き続ける（高価側）
//   - 秋の繰り下げ：2 回訪れる時刻に置いた cron はその日 2 回発火しうる
//     desired state は「直近に発火した側」だけで決まる冪等な導出なので、2 回目でも答えは変わらない
//
// つまり注意が要るのは春だけである
// DST のある地域では、切り替え帯（多くの地域で 01:00-03:00）を避けた時刻に cron を置くこと
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
	// 直近に発火した側の cron が決定権を持ち、同着なら stop に倒す（安全側かつ安価側)
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
