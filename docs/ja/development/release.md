# リリースと依存の更新

配布物、イメージのビルド、GHCR の初回公開、依存更新の設定を説明する。

GoReleaser は CI で実行し、ローカルでは実行しない。PR ごとの設定検証と、すべての CI 実行で行うスナップショットビルドは、[GitHub Actions](github-actions.md)を参照する。

## リリースの手順

Release Please は main への push と手動実行で未公開変更を確認し、fix・feat などがあればリリース PR を作成・更新する。定期実行は行わない。GITHUB_TOKEN による依存 PR の自動マージ後は、main、tag 空欄で手動実行する。

管理者は CI が承認待ちなら開始を承認し、バージョン、変更履歴、互換性を確認してリリース PR をマージする。マージに伴う main への push で公開処理が自動起動し、タグと draft Release を作成する。配布処理でタグとマージ SHA を照合してからビルド・配布する。

GoReleaser は既存 draft へアーカイブとチェックサムを添付し、変更履歴を維持する。コンテナはバージョン付きタグへ配布する。配布後に latest を更新し、GitHub Release を公開する。プレリリースでは latest を更新しない。

フロー図、停止、再実行の手順は [依存更新とリリース](../../dependency-release.md)を参照する。

## リリースの成果物

リリースの成果物は次のとおりである。

| 成果物 | 配置先 | イメージタグ |
|---|---|---|
| `cheapskate-reconciler` イメージ | `ghcr.io/moepig/cheapskate-reconciler` | バージョン、`latest` |
| `cheapskate-webconsole` イメージ | `ghcr.io/moepig/cheapskate-webconsole` | バージョン、`latest` |
| `cheapskate-cli` アーカイブ | GitHub リリース | — |

`latest` は試用と最新版の発見のために残す移動タグであり、デプロイには使わない。Lambda へ配置するイメージはバージョンタグ、厳密に同じ内容を再現する必要がある環境では digest で参照する。

どちらのイメージも `linux/amd64` と `linux/arm64` に対応するマルチアーキテクチャイメージである。

## リリースイメージのビルド

リリースイメージは、GoReleaser がクロスコンパイルしたバイナリから buildx が組み立てる。使用するファイルは `build/Dockerfile.reconciler` と `build/Dockerfile.webconsole` である。

ローカルビルドとイメージテストは、リポジトリルートの `Dockerfile` でソースからビルドする。実行手順は[ビルド](build.md)のコンテナイメージを参照する。

> [!IMPORTANT]
> リリース用の Dockerfile とルートの `Dockerfile` は、ランタイムステージが重複している。ベースイメージ、`ENV`、Lambda Web Adapter のバージョンを変更する場合は、両方に反映すること。

## GHCR の初回公開

GHCR への push はワークフローの `GITHUB_TOKEN` (`packages: write`) で行う。設定すべきシークレットはない。

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

dependabot/fetch-metadata が Dependabot コミットを検証し、更新種別を取得する。GitHub CLI で minor・patch の承認と squash auto-merge を予約する。major と種別不明の更新は管理者が確認して手動でマージする。

GitHub Actions 提供の ci、レビュー、最新ベース、push 後の承認無効化を設定してから DEPENDABOT_AUTO_MERGE を有効にする。アプリケーション依存は fix、Actions は chore を Dependabot の設定で指定する。依存ごとの影響解析やセキュリティ修正の根拠登録は行わない。

配布失敗時はタグ、draft、レジストリの状態を確認して再実行する。Release Please の tag 入力でも既存 draft を指定できる。バージョン付きイメージを置き換える可能性があるため、無条件には再実行しない。自動復旧や既存イメージの所有情報の照合は行わない。
