package compute

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
)

// タグを省略し、起動台数と上下限が 1、管理モードが scheduled となることを確認する。
func TestEcsDefaultConfiguration(t *testing.T) {
	config, err := ecsConfigFromTags(nil)
	require.NoError(t, err)
	assert.Equal(t, ecsConfig{desired: 1, minimum: 1, maximum: 1, boundsMode: ecsBoundsModeScheduled}, config)
}

// int32 の最大値と範囲外のタグ値を渡し、最大値を受け入れ、範囲外を拒否することを確認する。
func TestEcsConfigurationAcceptsInt32LimitAndRejectsOverflow(t *testing.T) {
	config, err := ecsConfigFromTags(map[string]string{
		model.EcsDesiredCountTagKey: "2147483647", model.EcsScalingMinTagKey: "2147483647", model.EcsScalingMaxTagKey: "2147483647",
	})
	require.NoError(t, err)
	assert.EqualValues(t, 2147483647, config.desired)

	_, err = ecsConfigFromTags(map[string]string{model.EcsDesiredCountTagKey: "2147483648"})
	assert.ErrorContains(t, err, "not an integer")
}
