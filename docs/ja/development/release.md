# リリースと依存の更新

goreleaser をローカルで実行することはない。pull request ごとに、CI が `goreleaser check` で `.goreleaser.yaml` を検証し、続けてスナップショットビルドを行う。後者はクロスコンパイルの全ターゲットと両方のイメージを実際に作るため、何も push せずにリリース経路の全体を通すことになる。これらを実行するワークフローは [github-actions.md](github-actions.md) に記述する。

## リリースの手順

```console
git tag v0.1.0
git push origin v0.1.0
```

バージョンはタグそのものである。goreleaser がタグから導出する。semver のプレリリース(`v0.1.0-rc.1`)であれば、GitHub リリースはプレリリースとして作られ、イメージの `latest` タグは移動しない。先にテストスイート全体がタグ付けされたコミットに対して走り、いずれかが失敗した場合、そのタグにリリースは作られない。

リリースの成果物:

| 成果物 | 配置先 | タグ |
|---|---|---|
| `cheapskate-reconciler` イメージ | `ghcr.io/moepig/cheapskate-reconciler` | バージョン、`latest` |
| `cheapskate-webconsole` イメージ | `ghcr.io/moepig/cheapskate-webconsole` | バージョン、`latest` |
| `cheapskate-cli` アーカイブ | GitHub リリース | — |

どちらのイメージもマルチアーキテクチャ(`linux/amd64`、`linux/arm64`)である。goreleaser がクロスコンパイルしたバイナリを、`build/Dockerfile.reconciler` と `build/Dockerfile.webconsole` により buildx が組み立てる。ソースからビルドするのはリポジトリルートの `Dockerfile` であり、ローカルのビルドとイメージテストにとってはそちらが正となる([build.md](build.md))。ランタイムのステージは両者で重複しているため、ベースイメージ・`ENV`・Lambda Web Adapter のバージョンの変更は、両方に反映する必要がある。

GHCR への push はワークフローの `GITHUB_TOKEN`(`packages: write`)で行う。設定すべきシークレットはない。

ただし初回リリースの後、イメージごとに一度だけ手作業が要る。作成された直後の GHCR のパッケージは、リポジトリの可視性に関わらず private である。パッケージが継承するのは、紐づいたリポジトリの権限であって可視性ではないためで、この状態では認証なしの `docker pull` は失敗する。リポジトリの Packages から各パッケージを public に変更する(パッケージの設定 → Change visibility)。2 回目以降のリリースは既存のパッケージに push するため、可視性はそのまま保たれる。

## 依存の更新

Dependabot が週次で pull request を作る(`.github/dependabot.yml`)。対象は Go モジュール、Actions、ルートおよびリリース用 Dockerfile のベースイメージである。常に一括で動くもの(AWS SDK のモジュール群、Actions、イメージ)が 1 つの pull request になるよう、グループ化している。いずれも `ci.yml` によって検査され、イメージテストも含まれる。

Lambda Web Adapter は Web コンソール用 Dockerfile にタグで固定されており、docker の更新対象に含まれる。Go の依存と異なり `go.mod` には現れないため、これがなければバージョンの移動に気づく仕組みが存在しない。

### リリースからの経過日数

いずれのエコシステムにも `cooldown.default-days: 7` を設定してある。公開から 1 週間が経つまで、Dependabot はそのバージョンを提案しない。汚染されたリリースは数日のうちに発見されて取り下げられることが多く、ここで 1 週間の距離を置く代償はない。これらの更新は自動でマージされるため、不正な公開と `main` の間に人の目が入らないからである。Dependabot は無設定でも 3 日の cooldown を適用する。7 日はそれを意図的に延ばしたものである。なお security update は cooldown の対象外であり、既知の CVE は即座に提案される。

### 自動マージ

`dependabot-auto-merge.yml` が Dependabot の pull request ごとに動き、patch と minor の更新について `gh pr merge --auto` を呼ぶ。major は対象外であり、人が読む。条件は「major でないもの」ではなく 2 つの更新種別の許可リストとしてあるため、種別を判定できなかった更新もここで止まる。グループ化された pull request では、メタデータはグループ中で最も大きい更新種別を報告する。したがって patch が 10 個あっても major が 1 つ混ざれば、グループ全体が保留される。

このワークフロー自体は何もマージしない。`--auto` は意思を記録するだけであり、実際のマージは必須チェックの通過後に GitHub が行う。つまり実質的な関門は `main` のブランチ保護である。以下の 2 つのリポジトリ設定が必要で、欠けると自動マージが機能しないか、無防備になる。

- Settings → General → Allow auto-merge を有効にする。無効だと `gh pr merge --auto` は失敗する。
- `main` に対するブランチ保護(または ruleset)で `ci.yml` のチェックを必須にする。選ぶべきチェック名は [github-actions.md](github-actions.md) にある。必須チェックが無い場合、`--auto` を設定した瞬間に pull request はマージされ、テストは飾りになる。
