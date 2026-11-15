# GitHub Actions

CI、依存更新の検証、レビュー済みリリースの公開を行う。PR の自動処理と Release Please は専用 App を使う。GoReleaser のビルド・公開ジョブには GITHUB_TOKEN の Contents: write と Packages: write を与える。App の鍵は別ジョブに置く。

## ワークフロー

| ファイル | 処理 |
| --- | --- |
| ci.yml | テスト・snapshot・必須 ci |
| test.yml | lint・単体・統合・イメージ・設定検査 |
| dependabot-auto-merge.yml | 署名検証・App の承認・予約 |
| release-please.yml | PR・draft・ビルド・検査・公開 |

CI は PR と main の push で実行する。ci は必要なジョブの失敗とスキップを失敗にする。統合テストとイメージテストは Docker を使い、AWS の資格情報を必要としない。外部 Action は完全長 SHA で固定し、checkout で資格情報を保存しない。

## マージと公開の条件

GitHub Actions 提供の ci、最新の main、レビュー、承認の無効化、Code Owner レビューを必須にする。自動マージはアプリケーション依存の minor・patch だけを対象とする。複数依存のグループ、Actions、Docker、major、種別不明、draft は手動判断とする。特権ジョブで PR のコードを checkout しない。

リリースは既存 Action と標準コマンドで行う。公開後は immutable releases で保護する。一部配布後の失敗は管理者が調査し、修正版を別バージョンで公開する。詳細は [依存更新とリリース](../../dependency-release.md)に従う。