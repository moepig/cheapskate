# 日常の操作

本ドキュメントは、設定と現在状態の確認、スケジュールの変更、一時的な起動・停止、ECS 設定の変更、および管理終了の手順を示す。

## 設定と現在状態の確認

CLI の接続先テーブルとタイムゾーンを設定する。確認用コマンドを、以下に示す。

```sh
export CHEAPSKATE_TABLE=cheapskate
export DEFAULT_TIMEZONE=Asia/Tokyo
cheapskate-cli list
cheapskate-cli -output json show --group dev
```

list で保存されたグループ設定を、show で所属リソースの設定タグと AWS の現在状態を確認する。show は起動・停止せず、状態が変わるまで待機もしない。show の JSON 出力で live_error があるリソースは、状態取得が失敗している。

Web 画面での確認と設定変更は、[Web コンソール](web_console.md)を参照。CLI の引数と出力形式は、[CLI リファレンス](cli.md)を参照。

## スケジュールの登録と変更

平日 09:00～18:00 に起動状態を維持する設定例を、以下に示す。

```console
cheapskate-cli schedule --group dev -start '0 9 * * 1-5' -stop '0 18 * * 1-5'
```

グループがなければ作成する。既存の override は保持されるため、有効な override がある間はスケジュールより優先される。失効済み override が残る場合は、先に clear-override で解除するか、新しい override へ置き換える。

## override による一時的な変更

スケジュールを残したまま、起動・停止の要求または自動制御の有効・無効を変更する。

### 稼働時間の延長

スケジュールを持つ dev を 2 時間起動状態に保つ例を、以下に示す。

```console
cheapskate-cli override --group dev running -for 2h
```

期限が切れると、その時点のスケジュールに従う状態へ戻る。期限付き override にはスケジュールが必要である。

### 停止状態の維持

無期限の停止要求を設定する例を、以下に示す。

```console
cheapskate-cli override --group dev stopped
```

### 自動制御の一時停止

リソースを手動操作するため、自動制御を無効にする例を、以下に示す。

```console
cheapskate-cli override --group dev disabled
```

disabled 自体は AWS リソースを変更しない。後続の状態確認と変更を抑止する。

### スケジュールへの復帰

スケジュールへ戻すコマンドを、以下に示す。

```console
cheapskate-cli clear-override --group dev
```

override だけのグループには使用できない。先にスケジュールを追加するか、別の override に置き換える。保存内容を含む例は、[CLI によるグループ設定例](cli_records.md)を参照。

## ECS の設定変更

ECS の設定は、DynamoDB のグループ上限と、service 自身のタグに分かれる。設定の意味と適用タイミングは、[ECS の起動・停止とスケーリング](ecs.md)を参照。

### ECS 台数上限の登録

ECS の起動を許可する前に、グループアイテムへ Number 型の ecs_max_count を登録する。既存の dev グループに上限 4 を登録する例を、以下に示す。

```sh
cheapskate-cli ecs-max-count --group dev 4
```

同じコマンドで上限を変更できる。CLI と Web コンソールのスケジュール・override 操作は上限を保持する。属性の削除と ECS 固有の設定は、[ECS service の設定](resources/ecs-service.md#dynamodb-と-cli) を参照。

### スケーリング上下限の管理方法の変更

タグの上下限を維持する運用と、AWS Scheduled Scaling による上下限変更を使用する運用を切り替える。タグ範囲を維持する場合は、service の cheapskate:scaling-bounds-mode を tags に設定し、グループの要求を running にする。正常なサイクルの後、通常の上下限と ScheduledScalingSuspended=true を確認する。

再開前に、cheapskate:scaling-min と cheapskate:scaling-max が目的の通常範囲であることを確認する。再開時にその範囲が適用され、範囲外の台数は AWS によって調整される。再開手順は、次のとおりである。

1. cheapskate:scaling-bounds-mode を scheduled に変更する。
2. グループの要求を running にする。
3. 正常なサイクルの後、show または Web コンソールで ScheduledScalingSuspended=false と通常の上下限を確認する。

停止要求が有効な間は、scheduled モードに変更しても AWS の一時停止は維持される。一時停止中に実行時刻を過ぎた scheduled action は再実行されない。

## 管理の終了

管理終了後に保つ起動・停止状態と、ECS の Scheduled Scaling の状態を先に決める。ECS を起動状態で残す場合は、維持する上下限を通常のタグに設定し、Scheduled Scaling を有効にするなら上下限管理モードを scheduled、タグ範囲を維持するなら tags にする。停止状態で残す場合は上下限 0/0 と一時停止が残る。

管理終了の手順は、次のとおりである。

1. 無期限の running または stopped override を設定する。
2. 書き込み完了から、設定済みの reconciler Lambda timeout 以上待つ。
3. reconciler を手動で呼び出すか、次の定期実行を待つ。
4. show で全リソースが指定した安定状態にあることを確認する。ECS は live.detail の上下限と ScheduledScalingSuspended も確認する。
5. リソースから cheapskate:group タグを削除する。
6. cheapskate-cli remove --group <グループ名> でグループを削除する。

この手順の途中では対象グループを変更しないこと。待機は、古い設定を読んだ実行が終了してから最終状態を確認するためである。グループ削除自体は AWS リソースを変更しない。

Terraform でグループを管理している場合は、設定変更と削除を Terraform で行う。手順は、[Terraform によるグループ設定](terraform_records.md)を参照。
