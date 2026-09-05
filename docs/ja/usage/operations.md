# 操作方法

`cheapskate-cli` と Web コンソールは、同じ検証および条件付き書き込みの application service を使用する。

## CLI

全体オプションは `-table <名前>` と `-output text|json` である。主な操作例を次に示す。

```console
cheapskate-cli list
cheapskate-cli show --group dev
cheapskate-cli schedule --group dev -start '0 8 * * 1-5' -stop '0 20 * * 1-5'
cheapskate-cli override --group dev running
cheapskate-cli override --group dev stopped -for 2h
cheapskate-cli override --group dev disabled
cheapskate-cli clear-override --group dev
cheapskate-cli remove --group dev
```

`schedule` はグループがなければ作成し、既存の場合は 2 個の cron 属性だけを変更する。無期限の `override` もグループを作成でき、override 属性だけを変更する。期限付き override には既存の schedule が必要である。`clear-override` にも schedule が必要である。`remove` はグループアイテム全体を条件付きで削除する。

変更前には必ず強い整合性の読み取りを行い、アイテム全体を検証する。条件付き書き込みの競合は自動的に再試行しない。schedule と override の属性は互いに維持される。同じ属性への 2 回の書き込みは、DynamoDB で後に適用された値になる。

CLI の終了コードを次の表に示す。

| コード | 意味 |
| --- | --- |
| `0` | 成功 |
| `1` | 引数、AWS、DynamoDB、または内部エラー |
| `2` | 不正な保存済みグループ、または条件付き書き込みの競合 |

text の `list` は有効なグループを stdout、不正な行を stderr へ出力する。JSON の `list` は、`groups` と `errors` を含む完全な object 1 個を stdout へ出力する。どちらも不正な行が 1 件以上あれば終了コード 2 となる。JSON の `show` は、不正な保存データに対して構造化された error object を出力し、終了コード 2 となる。

## Web コンソール

Web コンソールは、グループ一覧、グループ詳細、schedule form、および override form を提供する。詳細画面は、`cheapskate:group=<グループ>`、ECS 設定タグ、および読み取り専用の現在状態を表示する。日時の表示と解釈には `DEFAULT_TIMEZONE` を使用する。書き込みの競合には HTTP 409 を返す。

## 安全に管理を終了する手順

リソースを特定の状態に保って管理を終了する手順は次のとおりである。

1. 無期限の `running` または `stopped` override を設定する。
2. 書き込み完了から、設定済みの reconciler Lambda timeout 以上待つ。
3. reconciler を手動で呼び出すか、次の定期 invocation を待つ。
4. `show` で全リソースが指定した安定状態にあることを確認する。
5. リソースから所属タグを削除する。
6. グループを削除する。

この手順の途中では対象グループを変更しないこと。待機することで、古い設定を読んだ invocation が管理終了前に完了する。
