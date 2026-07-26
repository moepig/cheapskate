// Package image tests the container images cheapskate ships, from the outside.
//
// 対象はデプロイされる成果物そのものである
// Lambda ハンドラ、JSON の入出力契約、ビルドタグ（lambda.norpc）、イメージの中身は、ここを通ってはじめて検証される（単体テストと統合テストはどちらもパッケージを直接呼ぶ）
// webconsole イメージに同梱した Lambda Web Adapter 拡張も同じ理由でここにしか出てこない
// あれは Lambda 側にしか存在せず、ハンドラを直接呼ぶテストはアダプタを通らないためである
//
// そのため internal/ ではなくここに置いている
// 中の何も import せず、HTTP で外から叩くだけなので、イメージに含まれるどのパッケージにも属さない
//
// 動かすには Lambda ランタイムの代役が要る
// ベースイメージ同梱の Runtime Interface Emulator（RIE, /usr/local/bin/aws-lambda-rie）を使うが、それは手段であって検証の対象ではない
// 実際の Lambda と同じく /var/runtime/bootstrap を動かし、HTTP POST で呼び出す
//
// テスト本体は `image` ビルドタグの下にある（実行にイメージのビルドと docker が要るため）
// このファイルにタグが無いのは、タグ無しでビルドしたときにパッケージが空にならないようにするためである
// 空になると `go build ./...` や `go test ./...` が「Go のファイルが 1 つも無い」として失敗する
package image
