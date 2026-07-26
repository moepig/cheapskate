package model

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// グループが管理する AWS リソースを選ぶ条件
// TagKey=TagValue が付き、かつ種別が Types に含まれるリソースすべてが対象となる
// 対象は reconcile 時に Resource Groups Tagging API 経由で動的に探索される
type Selector struct {
	TagKey   string
	TagValue string
	Types    []ResourceType // 重複除去・ソート済み
}

// セレクタに設定が一切ないか（作成直後で未設定のグループか）を報告する
func (s Selector) Empty() bool {
	return s.TagKey == "" && s.TagValue == "" && len(s.Types) == 0
}

// セレクタが探索に使える状態かを検査する
// 値が空のタグフィルタは意図的にサポートしない
// Tagging API は Values を持たない TagFilter を「そのキーの任意の値」として扱う
// この曖昧さを cheapskate のセレクタモデルへ持ち込む価値はない
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

// 種別リストの重複を除去してソートし、その結果を返す
func normalizeTypes(types []ResourceType) []ResourceType {
	out := slices.Clone(types)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return slices.Compact(out)
}
