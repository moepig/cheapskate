package model

import "regexp"

// EC2 インスタンス
const TypeEc2Instance ResourceType = "ec2-instance"

// ec2-instance の宣言（resource.go の typeInfos に登録する）
// Ref はインスタンス ID そのものである
// タグ由来の設定は持たない
// ECS の desiredCount にあたる「元の規模」という概念がなく、start はインスタンス ID だけで足りるためである
var ec2InstanceType = TypeInfo{
	Type:        TypeEc2Instance,
	ARNService:  "ec2",
	ARNResource: "instance",
	RefPattern:  regexp.MustCompile(`^i-[0-9a-f]+$`),
}
