# ログ

reconciler と Web コンソールは、構造化ログを stderr へ出力する。reconcile のレコードは、必要に応じて `group`、`resource_id`、`desired`、`action`、および `error` を含む。

全体の読み込みを完了したサイクルの最後には、Describe まで到達したリソース数、成功したアクション数、およびグループまたはリソースのエラー数を含む `summary` レコードを出力する。設定の一括読み込みと全体のリソース検出に失敗した場合は、summary レコードを出力せず、Lambda のエラーとして返す。グループまたはリソース単位の失敗では、Lambda 呼び出し自体は成功する。

Start または Stop に成功した場合は `action`、失敗した場合は `group-error` または `resource-error` を出力する。DynamoDB にアクションやエラーの履歴を保存しないため、ログが継続的な運用記録となる。
