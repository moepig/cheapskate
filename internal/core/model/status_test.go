package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// グループ単位のステータスは合成リソース ID を通じて、個別リソースと同じ status# の形状と同じ通知重複排除の経路を共有する
// これは、合成 ID が実リソースの ID と衝突しないことにより成立する
// ScanAll はこの判定によりグループ行とリソース行を振り分ける (state.ScanAll を参照)
func TestGroupStatusIDRoundTripsAndNeverCollidesWithResources(t *testing.T) {
	for _, name := range []string{"dev", "a-b-c", "group"} {
		got, ok := GroupFromStatusID(GroupStatusID(name))
		assert.Truef(t, ok, "GroupStatusID(%q) must be recognised as a group", name)
		assert.Equal(t, name, got)
	}

	// 実リソースの ID は、いずれの種別でもグループとして解釈されてはならない
	for _, typ := range KnownTypes {
		id := Resource{Type: typ, Ref: "some/ref"}.ID()
		_, ok := GroupFromStatusID(id)
		assert.Falsef(t, ok, "resource id %q must never be read as a group", id)
	}
}
