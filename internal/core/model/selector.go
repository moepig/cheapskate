package model

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// グループが管理する AWS リソースを選択する条件
// TagKey=TagValue が付与され、かつ種別が Types に含まれるリソースのすべてが対象となる
// 対象は reconcile 時に Resource Groups Tagging API を通じて動的に探索する
type Selector struct {
	TagKey   string
	TagValue string
	Types    []ResourceType // 重複除去・ソート済み
}

// セレクタが未設定かどうかを報告する
func (s Selector) Empty() bool {
	return s.TagKey == "" && s.TagValue == "" && len(s.Types) == 0
}

// セレクタが探索に使用可能かを検査する
// 値が空のタグフィルタは受け付けない
// Tagging API は Values を持たない TagFilter を、そのキーの任意の値として扱う
// cheapskate のセレクタは、この解釈を採用しない
func (s Selector) Validate() error {
	if s.TagKey == "" {
		return fmt.Errorf("selector tag key is required")
	}
	if len(s.TagKey) > 128 {
		return fmt.Errorf("selector tag key too long (max 128): %q", s.TagKey)
	}
	if strings.HasPrefix(s.TagKey, "aws:") {
		return fmt.Errorf("selector tag key must not start with \"aws:\" (reserved): %q", s.TagKey)
	}
	if s.TagValue == "" {
		return fmt.Errorf("selector tag value is required")
	}
	if len(s.TagValue) > 256 {
		return fmt.Errorf("selector tag value too long (max 256): %q", s.TagValue)
	}
	if len(s.Types) == 0 {
		return fmt.Errorf("selector requires at least one resource type")
	}
	for _, t := range s.Types {
		if !t.Valid() {
			return fmt.Errorf("unknown resource type %q (want one of %v)", t, TypeNames(KnownTypes))
		}
	}
	return nil
}

// 種別リストの重複を除去してソートした結果を返す
func normalizeTypes(types []ResourceType) []ResourceType {
	out := slices.Clone(types)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return slices.Compact(out)
}
