package wire

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
)

// Targets は各ターゲットを、それ自身の Type() をキーにして並べる
// ここに不具合があると、たとえば同じ種別に 2 つのターゲットが登録されて片方が黙って隠れる
// その結果あるリソース種別が丸ごと reconciler の管理から外れ、他のどのテストでも捕まらない
func TestTargetsKeyedByOwnType(t *testing.T) {
	m := Targets(aws.Config{})

	require.Len(t, m, len(model.KnownTypes))
	for _, typ := range model.KnownTypes {
		tgt, ok := m[typ]
		require.True(t, ok, "no target wired for %s", typ)
		assert.Equal(t, typ, tgt.Type(), "target under key %s reports a different Type()", typ)
	}
}

// Describers は Targets とまったく同じ種別を網羅しなければならない
// フロントエンドが describe できない種別は、コンソールや `show` の出力でライブ状態が「不明」として現れる
func TestDescribersCoverEveryTargetType(t *testing.T) {
	d := Describers(aws.Config{})

	require.Len(t, d, len(model.KnownTypes))
	for _, typ := range model.KnownTypes {
		assert.Contains(t, d, typ)
	}
}
