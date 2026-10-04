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

## ECS 台数上限

ECS の起動を許可するには、グループアイテムに Number 型の `ecs_max_count` を登録すること。各 ECS サービスの desired count と scaling-max の両方に適用する上限であり、タグとは独立して保存する。既存の ECS 用グループにも登録が必要である。属性の制約は、[DynamoDB データモデル](../architecture/database.md)を参照。

既存の dev グループに上限 4 を登録する例を、以下に示す。

```sh
aws dynamodb update-item \
  --table-name cheapskate \
  --key '{"pk":{"S":"CONFIG"},"sk":{"S":"GROUP#dev"}}' \
  --update-expression 'SET ecs_max_count = :maximum' \
  --expression-attribute-values '{":maximum":{"N":"4"}}' \
  --condition-expression 'attribute_exists(pk)'
```

上限は DynamoDB または IaC で設定する。CLI と Web コンソールの schedule、override、および clear-override は上限を維持する。保存された上限は CLI の list/show と Web コンソールの詳細画面に表示される。

scheduled action の上下限は `ecs_max_count` の検証を通らない。Scheduled Scaling による台数の絶対上限としては使用できないため、scheduled action 自体の上下限も確認すること。

## ECS Scheduled Scaling の一時停止

ECS service の稼働中も Scheduled Scaling を一時停止するには、service に `cheapskate/scheduled-scaling-paused=true` を設定する。要求は ECS service の起動・停止をまたいで維持される。タグと AWS フラグの関係は、[リソースタグ](resource_tag.md#scheduled-scaling-の一時停止)を参照。

継続的な一時停止要求を解除する手順は、次のとおりである。

1. タグを `false` に変更するか削除する。
2. ECS service への要求を `running` にする。
3. 次の正常な reconcile の後、`cheapskate-cli -output json show --group <グループ>` または Web コンソールの観測結果で `ScheduledScalingSuspended=false` を確認する。

ECS service への要求が `stopped` の間は、タグを解除しても AWS フラグを `true` に維持する。再開時に、一時停止期間中の scheduled action は再実行されない。将来の実行時刻から再開する。[AWS の再開動作](https://docs.aws.amazon.com/autoscaling/application/userguide/application-auto-scaling-suspend-resume-scaling.html)

タグ変更は次の正常な reconcile で反映する。古い設定を取得済みの invocation は古い要求を適用しうる。AWS の一時停止フラグは新しい scheduled action の開始を抑止するが、開始済みの操作を取り消さない。

cheapskate 自体の実行停止では、ECS service と AWS フラグを変更しない。適用済みの AWS 設定は残るが、実行停止中のタグ変更や外部操作に対する再適用は、reconciler の実行再開まで行われない。

既存の AWS 側の一時停止を維持する場合は、一時停止制御を含むバージョンの配置前にタグを `true` にすること。未設定の既定値は `false` である。既存の一時停止中の正の固定範囲が通常の上下限タグと異なる場合は、起動途中として扱われるため、維持する通常の上下限をタグに設定するか、起動台数での再設定を許可する構成であることを確認すること。

## Web コンソール

Web コンソールは、グループ一覧、グループ詳細、schedule form、および override form を提供する。詳細画面は、`cheapskate:group=<グループ>`、ECS 設定タグ、および読み取り専用の現在状態を表示する。日時の表示と解釈には `DEFAULT_TIMEZONE` を使用する。書き込みの競合には HTTP 409 を返す。詳細画面の Until 欄は表示時刻の 2 時間後で初期化され、無期限の override を設定する場合は空欄にする。一覧画面の override form は無期限の override を作成する。

## 安全に管理を終了する手順

ECS の Scheduled Scaling を有効にして管理を終了する場合は、手順の開始前に一時停止要求を `false` にし、`running` override を選ぶこと。一時停止を維持して稼働状態で終了する場合はタグを `true` にする。ECS service の停止を維持して終了する場合は、タグ値によらず AWS の一時停止フラグが `true` となる。`disabled`、所属タグの削除、およびグループの削除は、AWS のフラグと上下限を復元しない。

リソースを特定の状態に保って管理を終了する手順は次のとおりである。

1. 無期限の `running` または `stopped` override を設定する。
2. 書き込み完了から、設定済みの reconciler Lambda timeout 以上待つ。
3. reconciler を手動で呼び出すか、次の定期 invocation を待つ。
4. `show` で全リソースが指定した安定状態にあることを確認する。ECS は JSON の live.Detail または Web コンソールで、AWS の一時停止フラグと上下限も確認する。
5. リソースから所属タグを削除する。
6. グループを削除する。

この手順の途中では対象グループを変更しないこと。待機することで、古い設定を読んだ invocation が管理終了前に完了する。
