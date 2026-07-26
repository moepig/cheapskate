// reconciler・CLI・web console が共有するドメインデータモデルを定義する
// 最内層のレイヤであり、cheapskate 内の何もインポートせず、これらがどう永続化されるかも知らない
// DynamoDB のアイテム形状とキー構成は internal/state 側にある
//
// ドメインの語彙はすべて名前付き型にしてある（ResourceType・Mode・DesiredState・ObservedState・Action）
// どれも下地は string だが、素の string のままにすると取り違えてもコンパイルが通ってしまう
// とくに DesiredState と ObservedState は "running"/"stopped" という同じ文字列を共有しているので、この 2 つが別の型であることだけが両者の混同を防いでいる（state.go の DecideAction を参照）
//
// 概念ごとにファイルを分けてある:
//
//	resource.go      リソース種別の宣言（TypeInfo）とその登録簿、探索で見つかったリソース（Resource）
//	resource_ec2.go  ec2-instance 固有の宣言
//	resource_ecs.go  ecs-service 固有の宣言（スケーリング設定のタグを含む）
//	resource_rds.go  rds-instance と rds-cluster 固有の宣言
//	selector.go      グループが管理するリソースを選ぶ条件
//	group.go         ターゲットグループの設定とその変更、および期限付きの上書き
//	state.go         望ましい状態・観測された状態・その差を埋める操作
//	status.go        reconciler が書き残す監査証跡と、その記録先の ID 空間
//
// 種別ごとに違うことは resource_<種別>.go だけに置く
// この分割の狙いは、対応リソース種別を足すときに触る場所を「そのファイル 1 つを増やして resource.go の登録簿へ 1 行足す」に閉じることである
// 探索・検証・列挙・表示はすべてその宣言から導かれるので、他のファイルに種別ごとの分岐は現れない
// （docs/ja/architecture/overview.md の「対応リソース種別を追加する」を参照）
package model
