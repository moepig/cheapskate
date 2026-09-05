# CLI アーキテクチャ

`cheapskate-cli` は `list`、`show`、`schedule`、`override`、`clear-override`、および `remove` を提供する。CLI と Web コンソールは同じ groups application service を呼び出すため、検証と条件付き書き込みの動作は同じである。

`-output text|json` で出力形式を選択する。JSON の `list` は、常に `groups` と `errors` を持つ 1 個の object を出力する。text の `list` は、有効なグループを stdout、不正なグループを stderr へ出力する。

終了コード 0 は成功、1 は引数、AWS、DynamoDB、または内部エラー、2 は保存済み設定の不正または条件付き書き込みの競合を表す。
