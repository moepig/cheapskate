# GitHub Actions

CI、依存更新、公開は GitHub Actions とリポジトリ設定で行う。認証は GITHUB_TOKEN だけを使い、独自の制御 Action は設けない。

## ワークフロー

| ファイル | 用途 |
| --- | --- |
| ci.yml | テスト、スナップショット、ci 集約結果 |
| test.yml | lint、単体・結合・イメージ・設定の検査 |
| dependabot-auto-merge.yml | メタデータ Action と GitHub auto-merge |
| release-please.yml | リリース PR、タグ、draft Release |
| distribute.yml | GoReleaser、チェックサム検証、公開 |

CI は main への push と PR で実行する。必要なジョブの失敗やスキップがあれば ci は失敗する。test.yml は test / lint、test / unit、test / integration、test / image、test / goreleaser を実行する。release-snapshot は全対象とイメージをビルドし、公開しない。結合・イメージテストは Docker を使い、AWS の認証情報を必要としない。

## マージ条件

main では GitHub Actions 提供の ci、最新ベースでの検証、レビューを必須にし、push 後の承認を無効にする。設定と CI を確認してから DEPENDABOT_AUTO_MERGE を有効にする。メタデータ Action が Dependabot コミットを検証し、GitHub CLI で minor・patch の承認と squash auto-merge を予約する。major と種別不明の更新は管理者が手動でマージする。

自動マージ用のワークフローでは PR のコードを checkout しない。独自の Check Run や差分解析は使わない。

## 公開

release-please がバージョン、変更履歴、タグ、draft Release を管理する。管理者がリリース PR をマージすると、タグとマージ SHA を配布処理へ渡す。GoReleaser がアーカイブとバージョン付きイメージを配布する。添付ファイルの SHA-256 を確認し、安定版の latest を更新してから draft を解除する。

失敗時はジョブを再実行するか、Release Please の tag 入力で既存 draft を指定する。専用の状態管理 Issue や自動復旧は設けない。制約と手順は [依存更新とリリース](../../dependency-release.md)に従う。

## ワークフローの変更

外部 Action は完全な SHA とバージョンコメントで固定する。ローカルの再利用可能ワークフローは呼び出し元のコミットで解決する。ci には必要な全ジョブを含め、結果を欠落させる paths フィルターを付けない。
