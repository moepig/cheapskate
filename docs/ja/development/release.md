# リリースと依存の更新

配布物、イメージのビルド、GHCR の初回公開、依存更新の設定を説明する。

GoReleaser は CI で実行し、ローカルでは実行しない。PR ごとの設定検証と、すべての CI 実行で行うスナップショットビルドは、[GitHub Actions](github-actions.md)を参照する。

## リリース

Release Please がリリース PR を作成する。人がレビューしてマージするとタグと draft が作られ、GoReleaser が CLI アーカイブと 2 種類のコンテナを配布して draft を公開する。

リリースは既存 Action と標準コマンドで行う。公開後は immutable releases で保護する。一部配布後の失敗は管理者が調査し、修正版を別バージョンで公開する。詳細は [依存更新とリリース](../../dependency-release.md)に従う。

## リリースの成果物

リリースの成果物は次のとおりである。

| 成果物 | 配置先 | イメージタグ |
|---|---|---|
| `cheapskate-reconciler` イメージ | `ghcr.io/moepig/cheapskate-reconciler` | バージョン |
| `cheapskate-webconsole` イメージ | `ghcr.io/moepig/cheapskate-webconsole` | バージョン |
| `cheapskate-cli` アーカイブ | GitHub リリース | — |

GHCR の latest は自動更新しない。Lambda へ配置するイメージはバージョンタグ、厳密に同じ内容を再現する必要がある環境では digest で参照する。

どちらのイメージも `linux/amd64` と `linux/arm64` に対応するマルチアーキテクチャイメージである。

## リリースイメージのビルド

リリースイメージは、GoReleaser がクロスコンパイルしたバイナリから buildx が組み立てる。使用するファイルは `build/Dockerfile.reconciler` と `build/Dockerfile.webconsole` である。

ローカルビルドとイメージテストは、リポジトリルートの `Dockerfile` でソースからビルドする。実行手順は[ビルド](build.md)のコンテナイメージを参照する。

> [!IMPORTANT]
> リリース用の Dockerfile とルートの `Dockerfile` は、ランタイムステージが重複している。ベースイメージ、`ENV`、Lambda Web Adapter のバージョンを変更する場合は、両方に反映すること。

## GHCR の初回公開

GHCR への push は公開ジョブの GITHUB_TOKEN の packages: write で行う。PR の自動処理と Release Please は、main 限定の automation Environment に置く専用 App の鍵を使う。

初回公開時の GHCR パッケージは既定で private になる。認証なしで取得できるよう、各パッケージの Settings で public に変更する。リポジトリの公開範囲だけではパッケージの公開範囲は変わらない。[GitHub のパッケージ公開設定](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)に従う。

2 回目以降は既存パッケージへ push するため、公開範囲を維持する。

## 依存の更新

Dependabot は `.github/dependabot.yml` に従って週次で PR を作成する。対象は Go モジュール、Actions、ルートおよびリリース用 Dockerfile のベースイメージである。

AWS SDK のモジュール群、Actions、イメージは、それぞれ一括更新する単位でグループ化する。更新はイメージテストを含む `ci.yml` の検査を受ける。

Dockerfile frontend、Go ビルダー、Lambda ベースイメージ、Lambda Web Adapter はタグと digest の両方で固定し、docker の更新対象に含める。Go の依存と異なり `go.mod` には現れないため、Dependabot がタグと digest を同時に更新する。

### リリースからの経過日数

Dependabot は公開から 7 日が経過したバージョンを提案する。いずれのエコシステムにも `cooldown.default-days: 7` を設定してあり、既定の 3 日の cooldown を延長している。

必要な CI とレビュー条件を満たした patch・minor 更新は自動マージする。公開直後の不正なリリースの発見や取り下げを待つため、更新提案までの待機期間を設ける。

> [!NOTE]
> セキュリティ更新は cooldown の対象外である。既知の CVE に対する更新は、公開からの日数によらず提案される。

### 自動マージ

metadata Action が Dependabot の作成者とコミット署名を検証する。専用 App がアプリケーション依存の minor・patch だけを承認し、squash auto-merge を予約する。複数依存のグループ、major、種別不明、Actions、Docker、draft は手動判断とする。保留には draft を使い、breaking-change は分類用とする。