package backoff

import (
	"context"
	"math/rand/v2"
	"time"
)

// 指数関数的に増える上限と full jitter を持つ待機ポリシー。
// 値は NewExponential で構築する。
type Exponential struct {
	initial time.Duration
	maximum time.Duration
	jitter  func(time.Duration) time.Duration
}

// initial から maximum まで増加する待機ポリシーを返す。
// initial と maximum は正数であり、initial は maximum 以下でなければならない。
func NewExponential(initial, maximum time.Duration) Exponential {
	if initial <= 0 {
		panic("backoff: initial duration must be positive")
	}
	if maximum < initial {
		panic("backoff: maximum duration must be at least the initial duration")
	}
	return Exponential{
		initial: initial,
		maximum: maximum,
		jitter: func(limit time.Duration) time.Duration {
			return time.Duration(rand.Int64N(int64(limit)))
		},
	}
}

// attempt に対応する full jitter の待機時間を返す。
// attempt が負数の場合は 0 として扱う。
func (b Exponential) Delay(attempt int) time.Duration {
	return b.jitter(b.ceiling(attempt))
}

// attempt に対応する時間だけ待機する。
// 待機中に ctx が終了した場合は ctx.Err() を返す。
func (b Exponential) Wait(ctx context.Context, attempt int) error {
	timer := time.NewTimer(b.Delay(attempt))
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b Exponential) ceiling(attempt int) time.Duration {
	limit := b.initial
	for range max(attempt, 0) {
		if limit >= b.maximum/2 {
			return b.maximum
		}
		limit *= 2
	}
	return min(limit, b.maximum)
}
