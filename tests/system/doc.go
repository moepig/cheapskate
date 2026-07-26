// Package system tests cheapskate as an assembled whole rather than package by package.
//
// 対象は「組み上がったもの」であって、どれか 1 つのパッケージの契約ではない
// reconcile ループに実アダプタ（state・aws/compute・aws/sns）を結線し、ローカルエミュレータを相手に 1 サイクル走らせる
// 本番で `internal/wire` が行う結線をテスト内で手で作るので、主語がどのパッケージでもない
// internal/ ではなくここに置いているのはそのためである
//
// 対になる判断として、`internal/state` と `internal/ui/cli` の統合テストは移していない
// あちらの対象は単一パッケージの契約であり（実 DynamoDB を相手にするかどうかは手段の違いにすぎない）、パッケージの隣に置くのが正しい
//
// エミュレータが要るだけで、ビルド成果物は要らないので、タグは統合テストと同じ `integration` である（成果物そのものを外から叩くのは tests/image）
// このファイルにタグが無いのは、タグ無しでビルドしたときにパッケージが空にならないようにするためである
package system
