# GitHub Actions

CI、依存 PR の自動マージ、リリース PR の作成、配布を実行するワークフローを説明する。認証には GITHUB_TOKEN を使う。

## ワークフロー

| ファイル | 用途 |
| --- | --- |
| ci.yml | テスト、スナップショット、ci 集約結果 |
| test.yml | lint、単体・結合・イメージ・設定の検査 |
| dependabot-auto-merge.yml | メタデータ Action と GitHub auto-merge |
| release-please.yml | リリース PR、タグ、draft Release |
| distribute.yml | GoReleaser、配布、公開 |

CI は main への push と PR で実行する。必要なジョブの失敗やスキップがあれば ci は失敗する。test.yml は test / lint、test / unit、test / integration、test / image、test / goreleaser を実行する。release-snapshot は全対象とイメージをビルドし、公開しない。結合・イメージテストは Docker を使い、AWS の認証情報を必要としない。

## マージ条件

main では GitHub Actions 提供の ci、最新ベースでの検証、レビューを必須にし、push 後の承認を無効にする。設定と CI を確認してから DEPENDABOT_AUTO_MERGE を有効にする。メタデータ Action が Dependabot コミットを検証し、GitHub CLI で minor・patch の承認と squash auto-merge を予約する。major と種別不明の更新は管理者が手動でマージする。

自動マージ用のワークフローでは PR のコードを checkout しない。独自の Check Run や差分解析は使わない。

## 公開

Release Please は main への push と手動実行で起動する。未公開の fix・feat などがあればリリース PR を作成・更新する。定期実行は行わない。GITHUB_TOKEN による依存 PR の自動マージ後は、main、tag 空欄で手動実行する。

管理者がリリース PR を main へマージすると、その push で自動起動してタグと draft Release を作成し、マージ SHA を配布処理へ渡す。GoReleaser がアーカイブとバージョン付きイメージを配布する。安定版の latest を更新してから draft を解除する。

失敗時はジョブを再実行するか、Release Please の tag 入力で既存 draft を指定する。専用の状態管理 Issue や自動復旧は設けない。PR 作成と公開のフロー図、停止と再実行の手順は [依存更新とリリース](../../dependency-release.md)を参照する。

## ワークフローの変更

外部 Action は完全な SHA とバージョンコメントで固定する。ローカルの再利用可能ワークフローは呼び出し元のコミットで解決する。ci には必要な全ジョブを含め、結果を欠落させる paths フィルターを付けない。
