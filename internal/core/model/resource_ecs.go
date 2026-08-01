package model

import "regexp"

// ECS サービス（クラスタに属する 1 サービス）
const TypeEcsService ResourceType = "ecs-service"

// EcsServiceTarget.Start がサービスを元の規模へ戻すために読む、ECS 専用のリソースタグ
// グループ単位の属性にはしていない
// グループのセレクタは複数の ECS サービスに同時にマッチしうるためである
// それらすべてを同じ desired count に揃えるのは誤りなので、セレクタ自身のタグの隣、AWS リソース側に置く
//
// この 3 つが「設定として意味を持つタグ」であることは下の ConfigTags が宣言しており、表示側はそちらだけを見る（Resource.Config を参照）
const (
	EcsDesiredCountTagKey = "cheapskate/desired-count"
	EcsScalingMinTagKey   = "cheapskate/scaling-min"
	EcsScalingMaxTagKey   = "cheapskate/scaling-max"
)

// ecs-service の宣言（resource.go の typeInfos に登録する）
// cheapskate が扱う種別のうち、Ref が 2 つの識別子からなるのはこれだけである
// ここが決めているのはその文法だけで、Ref を cluster と service へ分解するのは実際に ECS API を呼ぶ側（compute.EcsServiceTarget）にある
var ecsServiceType = TypeInfo{
	Type:        TypeEcsService,
	ARNService:  "ecs",
	ARNResource: "service",
	RefPattern:  regexp.MustCompile(`^[^/]+/[^/]+$`),
	// 短形式の service ARN はクラスタ名を含まないため、この形へ対応づけられない
	// リソースを黙って落とすのではなく、対処方法を示して明確に失敗させる
	RefHint: "the ecs service ARN may still use the old short format (no cluster name); enable long ARN " +
		"format with `aws ecs put-account-setting-default --name serviceLongArnFormat --value enabled`",
	ConfigTags: []ConfigTag{
		{Key: EcsDesiredCountTagKey, Name: "desired_count", Label: "desired"},
		{Key: EcsScalingMinTagKey, Name: "min", Label: "scaling min"},
		{Key: EcsScalingMaxTagKey, Name: "max", Label: "scaling max"},
	},
}
