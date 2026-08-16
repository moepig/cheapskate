package model

import "regexp"

// RDS の DB インスタンスと Aurora クラスタ
// 同一の AWS サービスに属し識別子の文法も共通であるが、stop/start の API が異なるため、別の種別として扱う
const (
	TypeRdsInstance ResourceType = "rds-instance"
	TypeRdsCluster  ResourceType = "rds-cluster"
)

// RDS の DB / クラスタ識別子の形式であり、AWS の制約に一致する
// 英字で始まり、英数字とハイフンのみを含む
var rdsIdentifierRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)

// rds-instance の宣言 (resource.go の typeInfos に登録する)
var rdsInstanceType = TypeInfo{
	Type:        TypeRdsInstance,
	ARNService:  "rds",
	ARNResource: "db",
	RefPattern:  rdsIdentifierRE,
}

// rds-cluster の宣言 (resource.go の typeInfos に登録する)
var rdsClusterType = TypeInfo{
	Type:        TypeRdsCluster,
	ARNService:  "rds",
	ARNResource: "cluster",
	RefPattern:  rdsIdentifierRE,
}
