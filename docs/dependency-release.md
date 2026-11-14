# 依存更新とリリース

Dependabot の依存更新を検証し、配布物に影響する変更を release-please のリリース PR にまとめる。認証には GITHUB_TOKEN だけを使う。リリース PR は管理者が確認してマージする。

## 公開対象と互換性

CLI の引数と出力、設定ファイル、Lambda の入出力、Web API、保存データ、最低 Go バージョンを互換性の対象とする。CLI の 6 種類のアーカイブと 2 種類のマルチプラットフォームイメージを配布する。AWS へのデプロイは既存の手順で行う。

## 設定

[依存判定と配布対象](../.github/dependency-release.json)に必須 CI、許可ファイル、配布物に影響しない依存を定義する。[release-please の設定](../release-please-config.json)と [manifest](../.release-please-manifest.json)で単一の製品バージョンを管理する。

CI の集約結果は ci とする。依存判定は dependency-review、公開可能な状態の確認は release-ready に記録する。すべて GitHub Actions 提供の必須チェックとして設定する。

## 運用

[共通の運用手順](https://github.com/moepig/flexi/blob/0de782a2a86360049265211687891636fb9c80a5/docs/dependency-release-operations.md)に従い、導入条件、CI の開始承認、公開、保留、復旧を確認する。セキュリティ修正の根拠登録は行わず、GitHub 標準の通知から公開時期を判断する。

GITHUB_TOKEN が作成・更新したリリース PR の CI は、管理者が GitHub 上で開始を承認する。配布処理はマージ SHA を指定して直接呼び出す。失敗時は draft を維持し、Release control の再実行で同じ版を回復する。
