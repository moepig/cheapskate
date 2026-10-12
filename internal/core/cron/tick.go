package cron

import (
	"errors"
	"time"

	"github.com/adhocore/gronx"
)

// reference のタイムゾーンで 5 フィールドの expr を解釈し、直前の発火時刻を返す。
// inclusive が true の場合は reference 自体の発火を含む。400 年以内に発火がない式はエラーを返す。
func PrevTickBefore(expr string, reference time.Time, inclusive bool) (time.Time, error) {
	if reference.Nanosecond() != 0 {
		reference = reference.Truncate(time.Second)
		inclusive = true
	}
	return findTick(expr, reference, inclusive, true)
}

// reference のタイムゾーンで 5 フィールドの expr を解釈し、直後の発火時刻を返す。
// inclusive が true の場合は reference 自体の発火を含む。400 年以内に発火がない式はエラーを返す。
func NextTickAfter(expr string, reference time.Time, inclusive bool) (time.Time, error) {
	if reference.Nanosecond() != 0 {
		reference = reference.Truncate(time.Second).Add(time.Second)
		inclusive = true
	}
	return findTick(expr, reference, inclusive, false)
}

// 時差が一定の区間ごとに探索し、元のタイムゾーンで発火時刻を返す。
func findTick(expr string, reference time.Time, inclusive, reverse bool) (time.Time, error) {
	location := reference.Location()
	search := gronx.NextTickAfter
	years := 400
	if reverse {
		search = gronx.PrevTickBefore
		years = -years
	}
	// 年指定のない暦は 400 年で反復する。時差変更で毎回欠落する式の探索にも上限を設ける。
	// https://go.dev/src/time/time.go: "The calendar runs on an exact 400 year cycle"
	limit := reference.UTC().AddDate(years, 0, 0)
	inRange := func(t time.Time) bool {
		return (reverse && !t.Before(limit)) || (!reverse && !t.After(limit))
	}
	for inRange(reference) {
		start, end := reference.ZoneBounds()
		// POSIX ルールによる年末の区間終端は、閏年でも 365 日で計算される場合がある。
		// https://go.dev/src/time/zoneinfo.go: "ystart + 365*secondsPerDay"
		if !end.IsZero() && !end.After(reference) {
			end = time.Date(reference.UTC().Year()+1, time.January, 1, 0, 0, 0, 0, time.UTC)
		}
		_, offset := reference.Zone()
		// 固定時差の区間では現地時刻が一意となり、暦によるジャンプでも候補の重複と欠落が生じない。
		fixed := time.FixedZone("", offset)
		tick, err := search(expr, reference.In(fixed), inclusive)
		if err != nil {
			return time.Time{}, err
		}
		if reverse {
			if start.IsZero() || !tick.Before(start) {
				if inRange(tick) {
					return tick.In(location), nil
				}
				break
			}
			reference = start.Add(-time.Nanosecond)
		} else {
			if end.IsZero() || tick.Before(end) {
				if inRange(tick) {
					return tick.In(location), nil
				}
				break
			}
			reference = end.In(location)
		}
		// 区間の境界に発火があれば候補に含める。
		inclusive = true
	}
	return time.Time{}, errors.New("no occurrence within 400 years")
}
