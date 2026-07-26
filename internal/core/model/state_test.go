package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDecideAction(t *testing.T) {
	cases := []struct {
		desired  DesiredState
		observed ObservedState
		want     Action
	}{
		{DesiredStopped, StateRunning, ActionStop},
		{DesiredRunning, StateStopped, ActionStart},
		{DesiredRunning, StateRunning, ActionNone}, // 収束済み
		{DesiredStopped, StateStopped, ActionNone}, // 収束済み
		// 遷移中と not-found はここでは何も決めない（呼び出し側が先に skip している）
		{DesiredStopped, StateTransitioning, ActionNone},
		{DesiredRunning, StateTransitioning, ActionNone},
		{DesiredStopped, StateNotFound, ActionNone},
		{DesiredNone, StateRunning, ActionNone}, // disabled のグループは何も望まない
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, DecideAction(c.desired, c.observed), "desired=%s observed=%s", c.desired, c.observed)
	}
}
