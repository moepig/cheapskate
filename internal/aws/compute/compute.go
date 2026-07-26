// cheapskate が管理するリソースについて、種別ごとの describe/stop/start 操作を実装する
// 対象は RDS インスタンスとクラスタ、ECS サービス、EC2 インスタンスである
// 1 種別につき 1 つの値を持ち、それぞれが port.Target を満たす
// これらのコントロールプレーン API に触れるのはこのパッケージだけである
package compute

//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks cheapskate/internal/aws/compute RdsAPI,EcsAPI,AutoScalingAPI,Ec2API
