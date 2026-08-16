// Package image tests the container images cheapskate ships, from the outside.
//
// 対象はデプロイする成果物そのものである
// Lambda ハンドラ、JSON の入出力契約、ビルドタグ (lambda.norpc)、イメージの内容は、本パッケージのみが検証する
// 単体テストと統合テストは、いずれもパッケージを直接呼ぶためである
// webconsole イメージへ同梱した Lambda Web Adapter 拡張も、同じ理由により本パッケージのみが対象とする
// この拡張は Lambda 側にのみ存在し、ハンドラを直接呼ぶテストはアダプタを経由しないためである
//
// したがって internal/ ではなく本ディレクトリへ置く
// 内部のパッケージを一切 import せず HTTP により外部から検証するため、イメージに含まれるいずれのパッケージにも属さない
//
// 実行には Lambda ランタイムの代替を要する
// ベースイメージへ同梱の Runtime Interface Emulator (RIE、/usr/local/bin/aws-lambda-rie) を用いるが、これは手段であり検証の対象ではない
// 実際の Lambda と同じく /var/runtime/bootstrap を起動し、HTTP POST で呼び出す
//
// テスト本体は `image` ビルドタグの下に置く。実行にイメージのビルドと docker を要するためである
// 本ファイルがタグを持たないのは、タグなしでビルドしたときにパッケージを空としないためである
// パッケージが空の場合、`go build ./...` と `go test ./...` は Go のファイルが存在しないとして失敗する
package image
