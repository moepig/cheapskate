# 依存更新とリリース

Dependabot、GitHub Actions、release-please の標準機能で依存更新と公開を行う。ワークフローの認証は GITHUB_TOKEN だけを使う。独自 Action や状態管理用アプリケーションは設けない。

## 公開対象と互換性

CLI の引数と出力、設定ファイル、Lambda の入出力、Web API、保存データ、最低 Go バージョンを互換性の対象とする。CLI の 6 種類のアーカイブと 2 種類のマルチプラットフォームイメージを配布する。AWS へのデプロイは既存の手順で行う。

## 設定

[Dependabot](../.github/dependabot.yml)に更新対象とコミットの prefix を定義する。[release-please 設定](../release-please-config.json)と [manifest](../.release-please-manifest.json)で単一の製品バージョンを管理する。必須 CI は ci とする。

アプリケーション依存の prefix は fix、Actions 更新は chore とする。依存ごとの配布物への影響は解析しないため、テスト専用依存でもリリース対象になり得る。ビルドに影響する Actions 更新を含める場合は、管理者が fix で始まる PR タイトルへ修正してマージする。

## 自動マージ

[Dependabot auto-merge](../.github/workflows/dependabot-auto-merge.yml)は fetch-metadata の署名検証と更新種別を使う。minor・patch を bot が承認し、指定した SHA の squash auto-merge を予約する。major、更新種別不明、draft、breaking-change ラベル付きの PR は管理者が確認して手動でマージする。

main に GitHub Actions 提供の ci、最新ベースでの検証、レビュー、push 後の承認無効化を設定する。Actions の PR 作成・承認も許可する。CI と保護設定を確認してから、リポジトリ変数 DEPENDABOT_AUTO_MERGE を true にする。

## 公開

[Release Please](../.github/workflows/release-please.yml)がリリース PR を作成する。GITHUB_TOKEN が作成・更新した PR の CI が承認待ちになった場合は、管理者が開始を承認する。バージョン、変更履歴、CI、互換性を確認してリリース PR を手動でマージする。

release-please がタグと draft Release を作成し、[配布処理](../.github/workflows/distribute.yml)を直接呼ぶ。タグとマージ SHA を照合し、配布の成功後に draft を解除する。公開済み Release へ再配布しない。

## 失敗時の再実行

失敗したジョブを再実行する。元の実行を使えない場合は、Release Please の手動起動で tag に既存 draft のタグを指定する。タグ、コミット、既存の配布物を確認してから操作する。未完了リリースを履歴から探す自動復旧は行わない。

公開を止める場合は Release Please を無効にし、実行中の処理を確認する。保留中はリリース PR をマージしない。

コンテナは GoReleaser がバージョン付きタグへ配布し、添付ファイルの検証後に latest を更新する。プレリリースでは latest を更新しない。draft の再配布では既存イメージを置き換える可能性があるため、状態を確認してから再実行する。

セキュリティ修正の独自判定と根拠登録は行わない。GitHub 標準の通知から公開時期を判断する。自動マージを止める場合は変数を false にし、既存 PR の予約も解除する。
