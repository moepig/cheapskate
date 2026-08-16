// Package system tests cheapskate as an assembled whole rather than package by package.
//
// 対象は組み上がった全体であり、個々のパッケージの契約ではない
// reconcile ループへ実アダプタ (state、aws/compute、aws/sns) を結線し、ローカルエミュレータに対して 1 サイクル実行する
// 本番で `internal/wire` が行う結線をテスト内で構築するため、対象が単一のパッケージに定まらない
// internal/ ではなく本ディレクトリへ置くのは、この理由による
//
// 一方、`internal/state` と `internal/ui/cli` の統合テストは移動しない
// これらの対象は単一パッケージの契約であり、実 DynamoDB を用いるかどうかは手段の違いにとどまるため、パッケージの隣へ置く
//
// 必要とするのはエミュレータのみでビルド成果物を要しないため、タグは統合テストと同じ `integration` とする
// 成果物を外部から検証するのは tests/image である
// 本ファイルがタグを持たないのは、タグなしでビルドしたときにパッケージを空としないためである
package system
