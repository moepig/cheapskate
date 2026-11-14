# リリースと依存の更新

本ドキュメントは、リリースの実行手順、リリースが生成する成果物、および依存更新の設定方針を記述する。

goreleaser は CI で実行し、ローカルでは実行しない。pull request ごとの設定検証と、すべての CI 実行で行うスナップショットビルドは、[GitHub Actions](github-actions.md)を参照する。

## リリースの手順

release-please がバージョンと変更履歴を含むリリース PR を作成する。管理者は CI の開始を承認し、内容をレビューして手動でマージする。制御処理はタグとマージ SHA を照合し、配布処理を直接呼び出す。

GoReleaser は既存 draft にアーカイブを添付し、release-please の変更履歴を維持する。コンテナはバージョン付きタグを先に登録し、全対象の検証後に latest を更新する。プレリリースでは latest を更新しない。添付ファイルと SHA-256 の検証後に GitHub Release を公開する。

設定、停止、手動再実行は [依存更新とリリース](../../dependency-release.md)に従う。

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

リリースイメージは、goreleaser がクロスコンパイルしたバイナリから buildx が組み立てる。使用するファイルは `build/Dockerfile.reconciler` と `build/Dockerfile.webconsole` である。

ローカルビルドとイメージテストは、リポジトリルートの `Dockerfile` でソースからビルドする。実行手順は[ビルド](build.md)のコンテナイメージを参照する。

> [!IMPORTANT]
> リリース用の Dockerfile とルートの `Dockerfile` は、ランタイムステージが重複している。ベースイメージ、`ENV`、Lambda Web Adapter のバージョンを変更する場合は、両方に反映すること。

## GHCR の初回公開

GHCR への push はワークフローの `GITHUB_TOKEN` (`packages: write`) で行う。設定すべきシークレットはない。

> [!IMPORTANT]
> 初回リリースの後、各 GHCR パッケージを一度だけ手作業で public に変更する必要がある。作成された直後の GHCR のパッケージは、リポジトリの可視性に関わらず private であり、認証なしの `docker pull` は失敗する。パッケージが継承するのは、紐づいたリポジトリの権限であって可視性ではないためである。リポジトリの Packages から各パッケージを public に変更すること (パッケージの設定 → Change visibility)。

2 回目以降のリリースは既存のパッケージに push するため、可視性はそのまま保たれる。

## 依存の更新

Dependabot は `.github/dependabot.yml` に従って週次で pull request を作成する。対象は Go モジュール、Actions、ルートおよびリリース用 Dockerfile のベースイメージである。

AWS SDK のモジュール群、Actions、イメージは、それぞれ一括更新する単位でグループ化する。更新はイメージテストを含む `ci.yml` の検査を受ける。

Dockerfile frontend、Go ビルダー、Lambda ベースイメージ、Lambda Web Adapter はタグと digest の両方で固定し、docker の更新対象に含める。Go の依存と異なり `go.mod` には現れないため、Dependabot がタグと digest を同時に更新する。

### リリースからの経過日数

Dependabot は公開から 7 日が経過したバージョンを提案する。いずれのエコシステムにも `cooldown.default-days: 7` を設定してあり、既定の 3 日の cooldown を延長している。

patch と minor の更新は人によるレビューを経ずに自動マージする。公開直後の不正なリリースの発見や取り下げを待つため、更新提案までの待機期間を設ける。

> [!NOTE]
> security update は cooldown の対象外である。既知の CVE に対する更新は、公開からの日数によらず提案される。

### 自動マージ

dependabot/fetch-metadata が Dependabot コミットを検証し、更新種別を取得する。GitHub CLI で minor・patch の承認と squash auto-merge を予約する。major と種別不明の更新は管理者が確認して手動でマージする。

GitHub Actions 提供の ci、レビュー、最新ベース、push 後の承認無効化を設定してから DEPENDABOT_AUTO_MERGE を有効にする。アプリケーション依存は fix、Actions は chore を Dependabot の設定で指定する。依存ごとの影響解析やセキュリティ修正の根拠登録は行わない。

配布失敗時はタグ、draft、レジストリの状態を確認して再実行する。Release Please の tag 入力でも既存 draft を指定できる。バージョン付きイメージを置き換える可能性があるため、無条件には再実行しない。自動復旧や既存イメージの所有情報の照合は行わない。
