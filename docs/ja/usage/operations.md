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

変更前には強い整合性の読み取りを行い、未知属性と属性型を検査する。schedule、override、および clear-override は変更後のグループ全体を検証する。remove は cron や属性間の不変条件を検証せず、読み取れたアイテムを削除する。失効済み override が残るグループの schedule 変更は、変更後の失効時刻が過去になるため拒否される。先に clear-override で削除するか、新しい override で置き換える。

条件付き書き込みの競合は自動的に再試行しない。schedule と override の属性は互いに維持される。同じ属性への 2 回の書き込みは、DynamoDB で後に適用された値になる。

同名グループの削除と再作成を、別の設定変更と同時に行わないこと。書き込み条件は属性の存在だけを確認するため、再作成された同名グループを古い要求が変更または削除する可能性がある。

CLI の終了コードを次の表に示す。

| コード | 意味 |
| --- | --- |
| `0` | 成功 |
| `1` | 引数、AWS、DynamoDB、または内部エラー |
| `2` | 不正な保存済みグループ、または条件付き書き込みの競合 |

text の `list` は有効なグループを stdout、不正な行を stderr へ出力する。JSON の list は、一覧の読み取りに成功した場合、groups と errors を含む完全な object 1 個を stdout へ出力する。読み取り自体の失敗は stderr へ出力し、終了コード 1 となる。どちらも不正な行が 1 件以上あれば終了コード 2 となる。JSON の `show` は、不正な保存データに対して構造化された error object を出力し、終了コード 2 となる。

変更後のグループの検証に失敗した場合は終了コード 1 となる。

show はリソースごとの Describe エラーを表示しても終了コード 0 となる。JSON では該当リソースの live_error に原因を出力するため、管理終了時は終了コードだけでなく各リソースの観測結果を確認すること。CLI の失効時刻は DEFAULT_TIMEZONE によらず UTC の RFC 3339 形式で表示する。

## Web コンソール

Web コンソールは、グループ一覧、グループ詳細、schedule form、および override form を提供する。詳細画面は、`cheapskate:group=<グループ>`、ECS 設定タグ、および読み取り専用の現在状態を表示する。日時の表示と解釈には `DEFAULT_TIMEZONE` を使用する。書き込みの競合には HTTP 409 を返す。詳細画面の Until 欄は表示時刻の 2 時間後で初期化され、無期限の override を設定する場合は空欄にする。一覧画面の override form は無期限の override を作成する。

## 安全に管理を終了する手順

リソースを特定の状態に保って管理を終了する手順は次のとおりである。

1. 無期限の `running` または `stopped` override を設定する。
2. 書き込み完了から、設定済みの reconciler Lambda timeout 以上待つ。
3. reconciler を手動で呼び出すか、次の定期 invocation を待つ。
4. `show` で全リソースが指定した安定状態にあることを確認する。
5. リソースから所属タグを削除する。
6. グループを削除する。

この手順の途中では対象グループを変更しないこと。待機することで、古い設定を読んだ invocation が管理終了前に完了する。
