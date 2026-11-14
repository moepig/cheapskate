# リリースと依存の更新

本ドキュメントは、リリースの実行手順、リリースが生成する成果物、および依存更新の設定方針を記述する。

goreleaser は CI で実行し、ローカルでは実行しない。pull request ごとの設定検証と、リリース経路の変更時に行うスナップショットビルドは、[GitHub Actions](github-actions.md)を参照する。

## リリースの手順

リリースは Git タグの push によって開始する。タグ付けされたコミットに対してテストスイート全体を実行し、いずれかが失敗した場合、そのタグにリリースは作られない。

```console
git tag v0.1.0
git push origin v0.1.0
```

バージョンは goreleaser が Git タグから導出する。semver のプレリリース (`v0.1.0-rc.1`) である場合、GitHub リリースはプレリリースとして作られ、イメージの `latest` タグは移動しない。

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

`dependabot-auto-merge.yml` は Dependabot の pull request ごとに実行する。patch と minor の更新について `gh pr merge --auto` を呼ぶ。自動マージの対象はこの 2 つの更新種別の許可リストで判定する。major は自動マージの対象外とし、人が確認する。種別を判定できなかった更新も自動マージしない。

グループ化された pull request では、メタデータはグループ中で最も大きい更新種別を報告する。したがって patch が 10 個あっても major が 1 つ混ざれば、グループ全体が保留される。

`--auto` は自動マージの設定を記録し、GitHub が必須チェックの通過後にマージする。必須チェックは `main` のブランチ保護で設定する。

自動マージには、次のリポジトリ設定が必要である。

- Settings → General → Allow auto-merge を有効にする。無効である場合、`gh pr merge --auto` は失敗する
- `main` に対するブランチ保護 (または ruleset) で `ci.yml` のチェックを必須にする。選択すべきチェック名は[GitHub Actions](github-actions.md)のチェック名を参照する

> [!WARNING]
> 必須チェックを設定しない場合、`--auto` を設定した時点で pull request はマージされる。テストの成功はマージ条件にならず、依存の更新が無検査で `main` に入る。
