package wire

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cheapskate/internal/core/model"
)

// Targets は各ターゲットを、それ自身の Type() をキーとして並べる
// この対応づけが誤っている場合、同じ種別へ 2 つのターゲットが登録され、一方が参照されなくなる
// その結果、あるリソース種別が reconciler の管理の対象から外れ、他のテストでは検出されない
func TestTargetsKeyedByOwnType(t *testing.T) {
	m := Targets(aws.Config{})

	require.Len(t, m, len(model.KnownTypes))
	for _, typ := range model.KnownTypes {
		tgt, ok := m[typ]
		require.True(t, ok, "no target wired for %s", typ)
		assert.Equal(t, typ, tgt.Type(), "target under key %s reports a different Type()", typ)
	}
}

// Describers は Targets と同一の種別を網羅しなければならない
// フロントエンドが describe できない種別は、コンソールと `show` の出力において現在状態が不明として現れる
func TestDescribersCoverEveryTargetType(t *testing.T) {
	d := Describers(aws.Config{})

	require.Len(t, d, len(model.KnownTypes))
	for _, typ := range model.KnownTypes {
		assert.Contains(t, d, typ)
	}
}
