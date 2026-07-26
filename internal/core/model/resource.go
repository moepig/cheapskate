package model

import (
	"fmt"
	"regexp"
	"slices"
)

// cheapskate が管理できる AWS リソースの種別
type ResourceType string

// リソース自身のタグ 1 つで与える、種別固有の設定項目の宣言
// Key は AWS リソースに付けるタグキー、Name は JSON 出力のキー、Label は人に見せる表示名である
// Name と Label を分けているのは、機械が読む名前を安定させたまま表示だけを読みやすくできるようにするためで、たとえば "min" というキーは web console の表では "scaling min" と出したい
//
// 値をどう解釈するかは各 port.Target の実装が決める（compute.EcsServiceTarget.Start を参照）
// ここにあるのは「どのタグが設定として意味を持つか」だけであり、だからこそ表示側は種別ごとの分岐を持たずに設定を出せる（Resource.Config を参照）
type ConfigTag struct {
	Key   string
	Name  string
	Label string
}

// リソース種別 1 つについて、種別ごとに異なるが振る舞いを伴わない事実をすべて宣言する
// 探索のしかた（ARN の形と Tagging API のフィルタ）、Ref の文法、タグで与える設定がこれにあたる
//
// これらを宣言として最内層に置くのは、同じ知識を必要とする側が層をまたいで散っているからである
// ARN の解析は internal/aws/tagging、設定の表示は internal/ui、Ref の検証はドメイン自身が使う
// 以前はそれぞれの場所に種別ごとの switch があり、種別を 1 つ足すと 5 か所を揃えて直す必要があった
// しかも揃っていないことにコンパイラも気づけず、探索や表示から種別が黙って抜け落ちうる形だった
//
// 逆に describe/stop/start という「振る舞い」はここには置かない
// AWS SDK のクライアントを要するのでこの層には入らず、port.Target として外側にある
type TypeInfo struct {
	Type ResourceType

	// ARN の service と resource-type であり、この組がリソース種別を一意に決める
	// 例: ("rds", "db")・("ecs", "service")
	ARNService  string
	ARNResource string

	// ARN から切り出した Ref が満たすべき形式
	// ここを通ったものだけが、各ターゲットの AWS API 呼び出しへ渡っていく
	RefPattern *regexp.Regexp
	// 形式に合わなかったときエラーへ添える、直し方の手がかり（不要なら空）
	RefHint string

	// この種別がリソース自身のタグから読む設定（持たない種別では空）
	ConfigTags []ConfigTag
}

// Resource Groups Tagging API の ResourceTypeFilters トークンを返す
// このトークンは "<service>:<resource-type>" であり、ARN の該当部分とそのまま一致する
// そのため別のフィールドとしては宣言せず、ここで導出する
// この対応が崩れる種別が現れたら、そのとき TypeInfo へ上書き用のフィールドを足せばよい
func (i TypeInfo) TaggingFilter() string { return i.ARNService + ":" + i.ARNResource }

// 対応する全リソース種別の登録簿であり、これが種別集合の唯一の定義である
// 各エントリの中身は種別ごとの resource_*.go にあり、ここはそれを並べるだけにしてある
// この 1 行が、その種別を探索・検証・列挙・表示のすべてに載せる
//
// 種別を足すときこのパッケージで触るのは、resource_*.go を 1 つ増やしてここへ 1 行足すことだけである
// あとは internal/aws/compute の port.Target 実装 1 つと internal/wire の結線 1 行になる
//
// 自動登録（各ファイルの init から register を呼ぶ）にはしていない
// 対応種別の一覧は cheapskate の仕様そのものなので、1 か所を読めば分かる形に保つ
var typeInfos = []TypeInfo{
	ec2InstanceType,
	ecsServiceType,
	rdsClusterType,
	rdsInstanceType,
}

// t の宣言を引く
// 未知の種別なら ok が false になる
func Info(t ResourceType) (TypeInfo, bool) {
	for _, i := range typeInfos {
		if i.Type == t {
			return i, true
		}
	}
	return TypeInfo{}, false
}

// ARN の service と resource-type から種別の宣言を引く（ARN 解析のための逆引き）
// tagging.ParseARN がこれを使い、種別ごとの switch を持たずに ARN を Resource へ移す
func InfoByARN(service, resource string) (TypeInfo, bool) {
	for _, i := range typeInfos {
		if i.ARNService == service && i.ARNResource == resource {
			return i, true
		}
	}
	return TypeInfo{}, false
}

// 全リソース種別をソート済みで並べたもの
// 既知かどうかの判定（Valid）も、CLI/UI での列挙（セレクタ種別のチェックボックス、フラグのヘルプ）も typeInfos からこれを通して導く
//
// "group" は意図的にこの集合へ含めない
// グループ自身のステータスは合成リソース ID である GroupNamespace+name（GroupStatusID を参照）で記録される
// この名前空間がリソースの "<type>#<ref>" 形式の ID と衝突しないのは、種別定数が決して "group" にならないからである
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

// types を文字列のスライスへ落とす（フラグのヘルプ文や、保存アイテムの文字列セット向け）
func TypeNames(types []ResourceType) []string {
	out := make([]string, len(types))
	for i, t := range types {
		out[i] = string(t)
	}
	return out
}

// 生の文字列（CLI のフラグ、フォームのチェックボックス、保存アイテム）を ResourceType へ移す
// 検証はしない
// 未知の種別を弾くのは Selector.Validate であり、そこでこそ「どの種別が既知か」をまとめて伝えられる
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
// 例: "ecs-service#dev-cluster/api"
func (r Resource) ID() string { return string(r.Type) + "#" + r.Ref }

// 種別と Ref の形式が噛み合っているかを、その種別の宣言（TypeInfo.RefPattern）に照らして検査する
// 探索がリソースを組み立てた時点で 1 回呼ぶ（tagging.ParseARN を参照）
//
// Ref の文法を決めているのは探索側（ARN の解析）だが、実際に使うのは操作側（AWS API の引数）である
// どちらか一方に置くと他方が同じ文法を再実装することになるので、判定はドメインに置く
// 探索時に確かめておけば、形式の誤りは stop/start を試みる瞬間ではなく、リソースが見つかった時点で分かる
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

// r のタグのうち、この種別が設定として扱うものを宣言の順に取り出す
// 未設定のタグは落とすので、設定が一つもなければ空を返す
//
// 表示専用（webconsole と `cheapskate-cli show` を参照）であり、解釈できない値も検証せずそのまま見せる
// 実際に妥当性を強制するのは各ターゲットの Start 側である
// タグを無条件に羅列するのではなく TypeInfo.ConfigTags を経由するのは、cheapskate が意味を与えているタグだけを設定として見せるためである
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

// タグから読んだ設定 1 件の、名前・表示名と生の値
type ConfigValue struct {
	Name  string
	Label string
	Value string
}
