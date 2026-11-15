# GitHub Actions

CI、依存更新の検証、レビュー済みリリースの公開を行う。PR の自動処理と Release Please は専用 App、ビルドは読み取り専用 GITHUB_TOKEN、公開は必要な書き込み権限を使う。

## ワークフロー

| ファイル | 処理 |
| --- | --- |
| ci.yml | テスト・snapshot・必須 ci |
| test.yml | lint・単体・統合・イメージ・設定検査 |
| dependabot-auto-merge.yml | 署名検証・App の承認・予約 |
| release-please.yml | PR・draft・ビルド・検査・公開 |

CI は PR と main の push で実行する。ci は必要なジョブの失敗とスキップを失敗にする。統合テストとイメージテストは Docker を使い、AWS の資格情報を必要としない。外部 Action は完全長 SHA で固定し、checkout で資格情報を保存しない。

## マージと公開の条件

GitHub Actions 提供の ci、最新の main、レビュー、承認の無効化、Code Owner レビューを必須にする。自動マージはアプリケーション依存の minor・patch だけを対象とする。Actions、Docker、major、種別不明、draft は手動判断とする。特権ジョブで PR のコードを checkout しない。

人が App のリリース PR をレビューしてマージすると、その push でタグと draft を作成する。読み取り権限で配布物をビルドし、別の公開ジョブがハッシュを照合する。公開記録を最初に添付し、不足分だけを追加する。最大の正式版だけで Latest を更新する。

有効化、停止、release_id による復旧は [依存更新とリリース](../../dependency-release.md)に従う。ci は必要なジョブに依存させ、必須チェックを欠落させる paths フィルターを付けない。
