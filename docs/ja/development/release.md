# リリースと依存の更新

本ドキュメントは、リリースの実行手順、リリースが生成する成果物、および依存更新の設定方針を記述する。

goreleaser をローカルで実行することはない。CI が pull request ごとに `goreleaser check` で `.goreleaser.yaml` を検証し、リリース経路に触れる pull request ではさらにスナップショットビルドを行う。後者はクロスコンパイルの全ターゲットと両方のイメージを実際に生成するため、何も push せずに経路の全体を通すことになる。

## リリースの手順

リリースはタグの push によって開始する。

```console
git tag v0.1.0
git push origin v0.1.0
```

バージョンはタグそのものであり、goreleaser がタグから導出する。semver のプレリリース(`v0.1.0-rc.1`)である場合、GitHub リリースはプレリリースとして作られ、イメージの `latest` タグは移動しない。先にテストスイート全体がタグ付けされたコミットに対して走り、いずれかが失敗した場合、そのタグにリリースは作られない。

リリースの成果物は次のとおりである。

| 成果物 | 配置先 | タグ |
|---|---|---|
| `cheapskate-reconciler` イメージ | `ghcr.io/moepig/cheapskate-reconciler` | バージョン、`latest` |
| `cheapskate-webconsole` イメージ | `ghcr.io/moepig/cheapskate-webconsole` | バージョン、`latest` |
| `cheapskate-cli` アーカイブ | GitHub リリース | — |

どちらのイメージもマルチアーキテクチャ(`linux/amd64`、`linux/arm64`)である。goreleaser がクロスコンパイルしたバイナリを、`build/Dockerfile.reconciler` と `build/Dockerfile.webconsole` により buildx が組み立てる。ソースからビルドするのはリポジトリルートの `Dockerfile` であり、ローカルのビルドとイメージテストにとってはそちらが正となる。ローカルビルドの手順は、[build.md](build.md) のコンテナイメージを参照。

> [!IMPORTANT]
> リリース用の Dockerfile とルートの `Dockerfile` は、ランタイムステージが重複している。ベースイメージ、`ENV`、Lambda Web Adapter のバージョンを変更する場合は、両方に反映すること。

GHCR への push はワークフローの `GITHUB_TOKEN`(`packages: write`)で行う。設定すべきシークレットはない。

> [!IMPORTANT]
> 初回リリースの後、イメージごとに一度だけ手作業を要する。作成された直後の GHCR のパッケージは、リポジトリの可視性に関わらず private であり、認証なしの `docker pull` は失敗する。パッケージが継承するのは、紐づいたリポジトリの権限であって可視性ではないためである。リポジトリの Packages から各パッケージを public に変更すること (パッケージの設定 → Change visibility)。

2 回目以降のリリースは既存のパッケージに push するため、可視性はそのまま保たれる。

## 依存の更新

Dependabot が週次で pull request を作る(`.github/dependabot.yml`)。対象は Go モジュール、Actions、ルートおよびリリース用 Dockerfile のベースイメージである。常に一括で動くもの(AWS SDK のモジュール群、Actions、イメージ)が 1 つの pull request になるよう、グループ化している。いずれも `ci.yml` によって検査され、イメージテストも含まれる。

Lambda Web Adapter は Web コンソール用 Dockerfile にタグで固定されており、docker の更新対象に含まれる。Go の依存と異なり `go.mod` には現れないため、これがなければバージョンの移動を検知する仕組みが存在しない。

### リリースからの経過日数

いずれのエコシステムにも `cooldown.default-days: 7` を設定してある。公開から 1 週間が経過するまで、Dependabot はそのバージョンを提案しない。これらの更新は自動でマージされ、不正な公開と `main` の間に人の目が入らないためである。汚染されたリリースは数日のうちに発見されて取り下げられることが多く、1 週間の距離を置く代償は小さい。Dependabot は無設定でも 3 日の cooldown を適用する。7 日はそれを意図的に延ばした値である。

> [!NOTE]
> security update は cooldown の対象外である。既知の CVE に対する更新は、公開からの日数によらず提案される。

### 自動マージ

`dependabot-auto-merge.yml` が Dependabot の pull request ごとに動作し、patch と minor の更新について `gh pr merge --auto` を呼ぶ。major は対象外であり、人が読む。条件は「major でないもの」ではなく 2 つの更新種別の許可リストとしてあるため、種別を判定できなかった更新もここで止まる。グループ化された pull request では、メタデータはグループ中で最も大きい更新種別を報告する。したがって patch が 10 個あっても major が 1 つ混ざれば、グループ全体が保留される。

このワークフロー自体は何もマージしない。`--auto` は意思を記録するのみであり、実際のマージは必須チェックの通過後に GitHub が行う。すなわち実質的な関門は `main` のブランチ保護である。前提となるリポジトリ設定を、以下に示す。

- Settings → General → Allow auto-merge を有効にする。無効である場合、`gh pr merge --auto` は失敗する
- `main` に対するブランチ保護(または ruleset)で `ci.yml` のチェックを必須にする。選択すべきチェック名は、[github-actions.md](github-actions.md) のチェック名を参照

> [!WARNING]
> 必須チェックを設定しない場合、`--auto` を設定した時点で pull request はマージされる。テストは検査として機能せず、依存の更新が無検査で `main` に入る。
