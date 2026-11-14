# GitHub Actions

CI、依存判定、リリースは .github/workflows の定義で実行する。認証には各ジョブの GITHUB_TOKEN を使う。

## ワークフロー

| ファイル | 用途 |
| --- | --- |
| ci.yml | テスト、スナップショット、ci 集約結果 |
| test.yml | lint、単体・結合・イメージ・設定の検査 |
| dependency-control.yml | 依存判定とマージ予約 |
| release-control.yml | リリース PR、復旧、公開 |
| distribute.yml | アーカイブとコンテナの配布 |

CI は main への push と PR で実行する。必要なジョブの失敗やスキップがあれば、集約結果の ci は失敗する。test.yml のジョブは test / lint、test / unit、test / integration、test / image、test / goreleaser として記録する。

release-snapshot は毎回すべての対象とイメージをビルドし、公開しない。結合・イメージテストはランナーの Docker デーモンを使い、AWS の認証情報を必要としない。

## 必須チェック

main では GitHub Actions 提供の ci、dependency-review、release-ready を必須にする。最新ベースでの検証とレビューを要求し、push 後の承認を無効にする。依存判定はこの設定を API で確認してから自動マージを予約する。CI の結果には現在の PR head に対する最新の試行を使う。

## 制御処理

依存判定は pull_request_target、workflow_run、定期実行、手動起動で行う。PR のコードを checkout せず、API で差分を読む。共通 Action は完全なコミット SHA で固定する。手動確認が必要な更新のレビューは、次の定期実行か手動起動で再評価する。

リリース制御は、マージ済みリリース PR の SHA を指定して distribute.yml を直接呼ぶ。GoReleaser は既存 draft と変更履歴を維持する。コンテナはバージョン付きタグを先に登録し、全配布物の検証後に latest を更新する。公開前の検証が成功してから draft を解除する。

CI の開始承認、保留、復旧は [依存更新とリリース](../../dependency-release.md)に従う。

各ワークフローの既定権限は contents: read とする。制御ジョブには API 操作に必要な書き込み権限を加える。配布ジョブは contents: write と packages: write を使う。追加のシークレットは不要である。

## ワークフローの変更

外部 Action は完全な SHA で固定し、Dependabot 用のバージョンコメントを残す。ローカルの再利用可能ワークフローは呼び出し元のコミットで解決する。ci のジョブ名を変更する場合は ciJobs の設定も更新する。必須ワークフローに、結果を欠落させる paths フィルターを付けない。
