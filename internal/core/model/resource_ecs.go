package model

import "regexp"

// ECS サービス (クラスタに属する 1 サービス)
const TypeEcsService ResourceType = "ecs-service"

// EcsServiceTarget.Start がサービスを元の規模へ戻すために読む、ECS 専用のリソースタグ
// グループ単位の属性とはしない
// グループのセレクタは複数の ECS サービスへ同時に一致しうるためである
// それらを同じ desired count へ揃えることは誤りであるため、AWS リソース側に置く
//
// この 3 つが設定として意味を持つタグであることは ConfigTags が宣言し、表示側はそちらを参照する (Resource.Config を参照)
const (
	EcsDesiredCountTagKey = "cheapskate/desired-count"
	EcsScalingMinTagKey   = "cheapskate/scaling-min"
	EcsScalingMaxTagKey   = "cheapskate/scaling-max"
)

// ecs-service の宣言 (resource.go の typeInfos に登録する)
// cheapskate が扱う種別のうち、Ref が 2 つの識別子からなるのはこの種別のみである
// ここで定めるのはその文法に限り、Ref の cluster と service への分解は ECS API を呼ぶ compute.EcsServiceTarget が行う
var ecsServiceType = TypeInfo{
	Type:        TypeEcsService,
	ARNService:  "ecs",
	ARNResource: "service",
	RefPattern:  regexp.MustCompile(`^[^/]+/[^/]+$`),
	// 短形式の service ARN はクラスタ名を含まないため、この形式へ対応づけられない
	// リソースをスキップせず、対処方法を示して失敗させる
	RefHint: "the ecs service ARN may still use the old short format (no cluster name); enable long ARN " +
		"format with `aws ecs put-account-setting-default --name serviceLongArnFormat --value enabled`",
	ConfigTags: []ConfigTag{
		{Key: EcsDesiredCountTagKey, Name: "desired_count", Label: "desired"},
		{Key: EcsScalingMinTagKey, Name: "min", Label: "scaling min"},
		{Key: EcsScalingMaxTagKey, Name: "max", Label: "scaling max"},
	},
}
