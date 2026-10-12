package cron

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// UTC で指定した発火時刻を比較し、重複・欠落する現地時刻と疎な暦の条件を確認する。
func TestTicksAcrossTimezoneAndCalendarBoundaries(t *testing.T) {
	tests := []struct {
		name, zone, expr, reference, previous, next string
	}{
		{"fallback", "America/New_York", "15 1 * * *", "2026-11-01T06:05:00Z", "2026-11-01T05:15:00Z", "2026-11-01T06:15:00Z"},
		{"spring gap", "America/New_York", "30 2 * * *", "2026-03-08T06:55:00Z", "2026-03-07T07:30:00Z", "2026-03-09T06:30:00Z"},
		{"half-hour fallback", "Australia/Lord_Howe", "45 1 * * *", "2026-04-04T15:05:00Z", "2026-04-04T14:45:00Z", "2026-04-04T15:15:00Z"},
		{"half-hour gap", "Australia/Lord_Howe", "15 2 * * *", "2026-10-03T15:25:00Z", "2026-10-02T15:45:00Z", "2026-10-04T15:15:00Z"},
		{"midnight fallback", "America/Havana", "15 0 * * *", "2026-11-01T05:05:00Z", "2026-11-01T04:15:00Z", "2026-11-01T05:15:00Z"},
		{"midnight gap", "America/Havana", "30 0 * * *", "2026-03-08T04:55:00Z", "2026-03-07T05:30:00Z", "2026-03-09T04:30:00Z"},
		{"date fallback", "America/Santiago", "15 23 * * *", "2026-04-05T03:05:00Z", "2026-04-05T02:15:00Z", "2026-04-05T03:15:00Z"},
		{"skipped date", "Pacific/Apia", "0 12 * * *", "2011-12-30T09:55:00Z", "2011-12-29T22:00:00Z", "2011-12-30T22:00:00Z"},
		{"annual fallback", "America/New_York", "15 1 1 11 *", "2026-11-01T06:05:00Z", "2026-11-01T05:15:00Z", "2026-11-01T06:15:00Z"},
		{"annual missing time", "America/New_York", "30 2 8 3 *", "2026-03-08T06:55:00Z", "2025-03-08T07:30:00Z", "2027-03-08T07:30:00Z"},
		{"century leap year", "America/New_York", "0 0 29 2 *", "2100-03-01T12:00:00Z", "2096-02-29T05:00:00Z", "2104-02-29T05:00:00Z"},
		{"future leap year end", "America/New_York", "0 0 1 1 *", "2040-12-31T12:00:00Z", "2040-01-01T05:00:00Z", "2041-01-01T05:00:00Z"},
		{"future southern year end", "Australia/Lord_Howe", "0 0 1 1 *", "2040-12-31T00:00:00Z", "2039-12-31T13:00:00Z", "2040-12-31T13:00:00Z"},
		{"month end", "America/New_York", "0 0 31 * *", "2026-04-01T04:00:00Z", "2026-03-31T04:00:00Z", "2026-05-31T04:00:00Z"},
		{"last day", "UTC", "0 9 L * *", "2026-09-03T12:00:00Z", "2026-08-31T09:00:00Z", "2026-09-30T09:00:00Z"},
		{"nearest weekday", "UTC", "0 9 15W * *", "2026-08-01T12:00:00Z", "2026-07-15T09:00:00Z", "2026-08-14T09:00:00Z"},
		{"nth weekday", "UTC", "0 9 * * MON#2", "2026-09-03T12:00:00Z", "2026-08-10T09:00:00Z", "2026-09-14T09:00:00Z"},
		{"day and weekday union", "UTC", "0 9 15 * MON", "2026-09-03T12:00:00Z", "2026-08-31T09:00:00Z", "2026-09-07T09:00:00Z"},
		{"Tokyo", "Asia/Tokyo", "0 9 * * MON-FRI", "2026-09-03T01:00:00Z", "2026-09-03T00:00:00Z", "2026-09-04T00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			location, err := time.LoadLocation(tt.zone)
			if err != nil {
				t.Fatal(err)
			}
			reference := parseTime(t, tt.reference).In(location)
			for _, direction := range []struct {
				search func(string, time.Time, bool) (time.Time, error)
				want   string
			}{{PrevTickBefore, tt.previous}, {NextTickAfter, tt.next}} {
				got, err := direction.search(tt.expr, reference, true)
				if err != nil {
					t.Fatal(err)
				}
				if !got.Equal(parseTime(t, direction.want)) || got.Location() != location {
					t.Fatalf("got %s in %s, want %s in %s", got, got.Location(), direction.want, location)
				}
			}
		})
	}
}

// 同じ現地時刻の各回と区間境界を別の発火として扱い、inclusive の指定を確認する。
func TestTickInclusionAcrossFallback(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	first := parseTime(t, "2026-11-01T05:00:00Z").In(location)
	second := parseTime(t, "2026-11-01T06:00:00Z").In(location)
	for _, tt := range []struct {
		search func(string, time.Time, bool) (time.Time, error)
		ref    time.Time
		incl   bool
		want   time.Time
	}{
		{NextTickAfter, first, false, second},
		{PrevTickBefore, second, false, first},
		{NextTickAfter, second, true, second},
		{PrevTickBefore, second, true, second},
		{PrevTickBefore, second.Add(-time.Second), true, first},
		{NextTickAfter, second.Add(-time.Second), true, second},
		{NextTickAfter, second.Add(-time.Nanosecond), false, second},
		{PrevTickBefore, second.Add(time.Nanosecond), false, second},
		{NextTickAfter, first.Add(time.Nanosecond), true, second},
	} {
		got, err := tt.search("0 1 * * *", tt.ref, tt.incl)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(tt.want) {
			t.Fatalf("reference=%s inclusive=%t: got %s, want %s", tt.ref, tt.incl, got, tt.want)
		}
	}
}

// UTC の各分を現地時刻に変換して日次式の発火を列挙し、探索結果が最も近い候補であることを確認する。
func TestDailyTicksAgainstAbsoluteTimeline(t *testing.T) {
	transitions := []struct{ zone, at string }{
		{"America/New_York", "2026-11-01T06:00:00Z"},
		{"America/New_York", "2026-03-08T07:00:00Z"},
		{"Australia/Lord_Howe", "2026-04-04T15:00:00Z"},
		{"Australia/Lord_Howe", "2026-10-03T15:30:00Z"},
		{"America/Havana", "2026-11-01T05:00:00Z"},
		{"America/Havana", "2026-03-08T05:00:00Z"},
		{"America/Santiago", "2026-04-05T03:00:00Z"},
		{"America/Santiago", "2026-09-06T04:00:00Z"},
		{"Pacific/Apia", "2011-12-30T10:00:00Z"},
	}
	for _, transition := range transitions {
		t.Run(transition.zone+"/"+transition.at, func(t *testing.T) {
			location, err := time.LoadLocation(transition.zone)
			if err != nil {
				t.Fatal(err)
			}
			edge := parseTime(t, transition.at)
			occurrences := make(map[string][]time.Time)
			for tick := edge.Add(-72 * time.Hour); !tick.After(edge.Add(72 * time.Hour)); tick = tick.Add(time.Minute) {
				local := tick.In(location)
				if local.Minute()%15 == 0 {
					expr := fmt.Sprintf("%d %d * * *", local.Minute(), local.Hour())
					occurrences[expr] = append(occurrences[expr], tick)
				}
			}
			for reference := edge.Add(-2 * time.Hour); !reference.After(edge.Add(2 * time.Hour)); reference = reference.Add(15 * time.Minute) {
				for expr, ticks := range occurrences {
					for _, inclusive := range []bool{false, true} {
						prevIndex := sort.Search(len(ticks), func(i int) bool {
							return ticks[i].After(reference) || (!inclusive && ticks[i].Equal(reference))
						}) - 1
						nextIndex := sort.Search(len(ticks), func(i int) bool {
							return ticks[i].After(reference) || (inclusive && ticks[i].Equal(reference))
						})
						for _, direction := range []struct {
							search func(string, time.Time, bool) (time.Time, error)
							index  int
						}{{PrevTickBefore, prevIndex}, {NextTickAfter, nextIndex}} {
							if direction.index < 0 || direction.index >= len(ticks) {
								t.Fatal("reference window has no expected occurrence")
							}
							got, err := direction.search(expr, reference.In(location), inclusive)
							want := ticks[direction.index]
							if err != nil || !got.Equal(want) {
								t.Fatalf("expr=%q reference=%s inclusive=%t: got %s, error=%v, want %s", expr, reference.In(location), inclusive, got, err, want)
							}
						}
					}
				}
			}
		})
	}
}

// 構文不正と暦上到達不能な式について、両方向の探索がエラーを返すことを確認する。
func TestUnreachableTicks(t *testing.T) {
	reference := parseTime(t, "2026-11-01T06:05:00Z")
	for _, expr := range []string{"invalid", "0 0 31 2 *"} {
		for _, search := range []func(string, time.Time, bool) (time.Time, error){PrevTickBefore, NextTickAfter} {
			if _, err := search(expr, reference, true); err == nil {
				t.Fatalf("expr=%q: want an error", expr)
			}
		}
	}
}

// 毎年の発火が夏時間の欠落時間に重なる式を、未来方向では到達不能として拒否する。
// 過去方向では夏時間のルール変更前の発火を取得する。
func TestAnnualTickAlwaysInSpringGap(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	reference := parseTime(t, "2026-03-01T12:00:00Z").In(location)
	expr := "30 2 * 3 SUN#2"
	if got, err := NextTickAfter(expr, reference, true); err == nil {
		t.Fatalf("got %s, want an error for an annual occurrence in the spring gap", got)
	}
	got, err := PrevTickBefore(expr, reference, true)
	if err != nil {
		t.Fatal(err)
	}
	want := parseTime(t, "2006-03-12T07:30:00Z")
	if !got.Equal(want) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func parseTime(t *testing.T, raw string) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
