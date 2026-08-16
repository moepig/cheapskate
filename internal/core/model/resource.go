package model

import (
	"fmt"
	"regexp"
	"slices"
)

// cheapskate が管理できる AWS リソースの種別
type ResourceType string

// リソース自身のタグ 1 つで与える、種別固有の設定項目の宣言
// Key は AWS リソースへ付与するタグキー、Name は JSON 出力のキー、Label は表示名である
// Name と Label を分けるのは、機械が読む名前を固定したまま表示名を変更できるようにするためである
//
// 値の解釈は各 port.Target の実装が定める (compute.EcsServiceTarget.Start を参照)
// ここで宣言するのは、設定として意味を持つタグの集合のみである
// これにより表示側は、種別ごとの分岐なしに設定を出力できる (Resource.Config を参照)
type ConfigTag struct {
	Key   string
	Name  string
	Label string
}

// リソース種別 1 つについて、種別ごとに異なり、かつ振る舞いを伴わない事実をすべて宣言する
// 該当するのは、探索の方法 (ARN の形式と Tagging API のフィルタ)、Ref の文法、タグで与える設定である
//
// これらを宣言として最内層へ置くのは、同じ知識を必要とする側が複数の層に存在するためである
// ARN の解析は internal/aws/tagging、設定の表示は internal/ui、Ref の検証はドメイン自身が用いる
// 種別ごとの switch を各所に置いた場合、種別の追加は 5 か所の変更を要する
// またその不整合をコンパイラが検出できず、探索や表示から種別が欠落しうる
//
// describe/stop/start の振る舞いはここへ置かない
// AWS SDK のクライアントを必要とするためこの層には含まず、port.Target として外側に置く
type TypeInfo struct {
	Type ResourceType

	// ARN の service と resource-type であり、この組がリソース種別を一意に決定する
	ARNService  string
	ARNResource string

	// ARN から切り出した Ref が満たすべき形式
	// これを満たす Ref のみが、各ターゲットの AWS API 呼び出しへ渡る
	RefPattern *regexp.Regexp
	// 形式に一致しなかった場合にエラーへ添える、対処方法 (不要な場合は空)
	RefHint string

	// この種別がリソース自身のタグから読む設定 (持たない種別では空)
	ConfigTags []ConfigTag
}

// Resource Groups Tagging API の ResourceTypeFilters トークンを返す
// このトークンは "<service>:<resource-type>" であり、ARN の該当部分と一致する
// したがって独立したフィールドとしては宣言せず、ここで導出する
// この対応が成立しない種別が生じた場合は、TypeInfo へ上書き用のフィールドを追加する
func (i TypeInfo) TaggingFilter() string { return i.ARNService + ":" + i.ARNResource }

// 対応する全リソース種別の登録簿であり、種別集合の唯一の定義である
// 各エントリの内容は種別ごとの resource_*.go にあり、ここではそれを列挙する
// ここへの登録により、その種別が探索、検証、列挙、表示のすべての対象となる
//
// 種別の追加に伴う本パッケージの変更は、resource_*.go の追加とここへの 1 行の追加に限られる
// 他に必要となるのは、internal/aws/compute の port.Target 実装 1 つと internal/wire の結線 1 行である
//
// 各ファイルの init から register を呼ぶ自動登録は行わない
// 対応種別の一覧は cheapskate の仕様であり、1 か所で参照できる形を保つためである
var typeInfos = []TypeInfo{
	ec2InstanceType,
	ecsServiceType,
	rdsClusterType,
	rdsInstanceType,
}

// t の宣言を返す
// 未知の種別の場合は ok が false となる
func Info(t ResourceType) (TypeInfo, bool) {
	for _, i := range typeInfos {
		if i.Type == t {
			return i, true
		}
	}
	return TypeInfo{}, false
}

// ARN の service と resource-type から種別の宣言を返す
// tagging.ParseARN がこれを用い、種別ごとの switch を持たずに ARN を Resource へ変換する
func InfoByARN(service, resource string) (TypeInfo, bool) {
	for _, i := range typeInfos {
		if i.ARNService == service && i.ARNResource == resource {
			return i, true
		}
	}
	return TypeInfo{}, false
}

// 全リソース種別をソートして並べたもの
// 既知かどうかの判定 (Valid) と、CLI/UI での列挙は、いずれも typeInfos からこれを通じて導出する
//
// "group" はこの集合へ含めない
// グループ自身のステータスは、合成リソース ID である GroupNamespace+name で記録する (GroupStatusID を参照)
// この名前空間がリソースの "<type>#<ref>" 形式の ID と衝突しないのは、種別定数が "group" とならないためである
var KnownTypes = knownTypes()

func knownTypes() []ResourceType {
	out := make([]ResourceType, 0, len(typeInfos))
	for _, i := range typeInfos {
		out = append(out, i.Type)
	}
	slices.Sort(out)
	return out
}

// t が既知のリソース種別かを報告する
func (t ResourceType) Valid() bool {
	_, ok := Info(t)
	return ok
}

// types を文字列のスライスへ変換する
func TypeNames(types []ResourceType) []string {
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = string(t)
	}
	return out
}

// 文字列を ResourceType へ変換する
// 検証は行わない
// 未知の種別の拒否は Selector.Validate が行う。既知の種別の一覧をまとめて示せるためである
func ResourceTypes(names []string) []ResourceType {
	out := make([]ResourceType, len(names))
	for i, n := range names {
		out[i] = ResourceType(n)
	}
	return out
}

// グループのセレクタによって発見された AWS リソース 1 件
type Resource struct {
	Type ResourceType
	Ref  string // ターゲット固有の識別子: "db1", "dev-cluster/api", "i-0abc123"
	ARN  string
	Tags map[string]string // セレクタの tag_key/tag_value に限らない、リソースに付いた全タグ
}

// ステータスレコードと通知におけるリソースの識別子を返す
// 形式は "<type>#<ref>" である
func (r Resource) ID() string { return string(r.Type) + "#" + r.Ref }

// 種別と Ref の形式の対応を、その種別の宣言 (TypeInfo.RefPattern) に照らして検査する
// 探索がリソースを組み立てた時点で 1 回呼ぶ (tagging.ParseARN を参照)
//
// Ref の文法を定めるのは探索側の ARN 解析であり、それを用いるのは操作側の AWS API 呼び出しである
// いずれか一方へ置いた場合、他方が同じ文法を再実装するため、判定はドメインへ置く
// 探索時に検査することにより、形式の誤りは stop/start の実行時ではなく、リソースの発見時に判明する
func (r Resource) Validate() error {
	info, ok := Info(r.Type)
	if !ok {
		return fmt.Errorf("unknown resource type %q", r.Type)
	}
	if r.Ref == "" {
		return fmt.Errorf("%s: ref is required", r.Type)
	}
	if !info.RefPattern.MatchString(r.Ref) {
		err := fmt.Errorf("%s: ref %q does not match %s", r.Type, r.Ref, info.RefPattern)
		if info.RefHint != "" {
			return fmt.Errorf("%w; %s", err, info.RefHint)
		}
		return err
	}
	return nil
}

// r のタグのうち、この種別が設定として扱うものを宣言の順に返す
// 未設定のタグは除外するため、設定が存在しない場合は空を返す
//
// 表示専用であり、解釈できない値も検証せずそのまま返す
// 妥当性の強制は各ターゲットの Start が行う
// タグを列挙せず TypeInfo.ConfigTags を経由するのは、cheapskate が意味を定義したタグのみを設定として扱うためである
func (r Resource) Config() []ConfigValue {
	info, ok := Info(r.Type)
	if !ok {
		return nil
	}
	var out []ConfigValue
	for _, c := range info.ConfigTags {
		if v := r.Tags[c.Key]; v != "" {
			out = append(out, ConfigValue{Name: c.Name, Label: c.Label, Value: v})
		}
	}
	return out
}

// タグから読んだ設定 1 件の名前、表示名、および値
type ConfigValue struct {
	Name  string
	Label string
	Value string
}
