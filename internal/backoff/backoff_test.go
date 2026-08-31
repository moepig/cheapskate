package backoff

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 各 attempt の jitter 上限が initial から倍増し、maximum で停止することを確かめる。
func TestExponentialDelayUsesCappedAttemptCeiling(t *testing.T) {
	b := NewExponential(10*time.Millisecond, 40*time.Millisecond)
	var limits []time.Duration
	b.jitter = func(limit time.Duration) time.Duration {
		limits = append(limits, limit)
		return limit - 1
	}

	var delays []time.Duration
	for _, attempt := range []int{-1, 0, 1, 2, 10} {
		delays = append(delays, b.Delay(attempt))
	}

	assert.Equal(t, []time.Duration{10 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 40 * time.Millisecond}, limits)
	assert.Equal(t, []time.Duration{10*time.Millisecond - 1, 10*time.Millisecond - 1, 20*time.Millisecond - 1, 40*time.Millisecond - 1, 40*time.Millisecond - 1}, delays)
}

// context の終了後は長い待機時間を消費せず、終了理由を返すことを確かめる。
func TestExponentialWaitHonorsContext(t *testing.T) {
	b := NewExponential(time.Hour, time.Hour)
	b.jitter = func(time.Duration) time.Duration { return time.Hour }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.ErrorIs(t, b.Wait(ctx, 0), context.Canceled)
}
