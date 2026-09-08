# cheapskate slim 目標仕様

## 概要

本ドキュメントは、Status、doctor、DynamoDB リース、および任意の selector を持たない cheapskate の目標仕様を定める。schedule と override によって、固定タグが付いた RDS、Aurora、ECS、および EC2 を起動状態または停止状態へ収束させる。

## 設計境界

維持する機能と削除する機能を、以下にまとめる。

| 維持する機能 | 削除する機能 |
| --- | --- |
| schedule と override | mode |
| グローバルタイムゾーン | グループ単位の timezone |
| 固定された group tag | 任意の tag key、tag value、resource types |
| 操作別の条件付きグループ更新 | DynamoDB リース |
| 設定とリソースのサイクル単位の一括読み込み | selector claims とグループ間の優先順位 |
| 構造化ログ、任意のメトリクス、アクション通知 | Status、doctor、エラー通知、復旧通知 |

複数の reconcile は同時に実行されうる。同じ絶対状態への Start または Stop が重複しても、古い設定 snapshot を持つ invocation の終了後に成功する reconcile が現在の設定へ収束させることを前提とする。

## 実行時設定

プロセス単位の設定を、以下に示す。

| 環境変数 | 必須 | 既定値 | 用途 |
| --- | :---: | --- | --- |
| `STATE_TABLE_NAME` | はい | なし | state テーブル名 |
| `DEFAULT_TIMEZONE` | いいえ | `UTC` | 全 schedule と Web コンソールの日時に適用する IANA タイムゾーン |
| `METRICS_ENABLED` | いいえ | `false` | CloudWatch カスタムメトリクスの出力 |
| `METRICS_NAMESPACE` | いいえ | `cheapskate` | カスタムメトリクスの名前空間 |
| `NOTIFICATION_TOPIC_ARN` | いいえ | なし | アクション通知先の SNS topic |

`DEFAULT_TIMEZONE` は reconciler と Web コンソールの起動時に `time.LoadLocation` で検証する。不正な値では起動を失敗させる。Web コンソールの未設定時に `time.Local` は使用しない。

`METRICS_ENABLED` が不正な boolean の場合は起動を失敗させる。未設定時はメトリクスを出力しない。

## DynamoDB

### テーブル構成

state テーブルの構成を、以下に示す。

| 項目 | 値 |
| --- | --- |
| partition key | `pk`、String |
| sort key | `sk`、String |
| GSI / LSI | なし |
| TTL | 使用しない |

保存するアイテムはグループアイテムだけである。1 グループを `pk=CONFIG`、`sk=GROUP#<name>` の 1 アイテムで表す。

全グループの列挙には `CONFIG` partition の Query を使用し、Scan は使用しない。`GetItem` は未知の sort key を列挙できず、`BatchGetItem` は別途キー一覧を必要とするため採用しない。全グループを 1 アイテムへ格納する方式も、グループ数に応じて毎回の書き込み量が増え、アイテムサイズの上限を共有するため採用しない。1 グループ 1 アイテムの Query が、補助索引やキー一覧を持たない最小の構成である。

### グループアイテム

グループアイテムの属性を、以下に示す。

| 属性 | 型 | 必須 | 意味 |
| --- | --- | :---: | --- |
| `pk` | S | はい | `CONFIG` |
| `sk` | S | はい | `GROUP#<name>` |
| `start_cron` | S | 条件付き | 起動 schedule |
| `stop_cron` | S | 条件付き | 停止 schedule |
| `override` | S | いいえ | `running`、`stopped`、`disabled` |
| `override_expires_at` | N | いいえ | override の失効時刻を表す Unix time の秒 |

グループアイテムの例を、以下に示す。

```json
{
  "pk": {"S": "CONFIG"},
  "sk": {"S": "GROUP#dev"},
  "start_cron": {"S": "0 9 * * MON-FRI"},
  "stop_cron": {"S": "0 20 * * MON-FRI"},
  "override": {"S": "running"},
  "override_expires_at": {"N": "1788159600"}
}
```

許可する属性は表に記載した属性だけである。未知属性は無視せず、当該グループの設定エラーとする。属性名の入力ミスによって期限や schedule が欠落した状態を有効な設定として扱わないためである。

### グループ検証

グループアイテムの不変条件は次のとおりである。

- グループ名は `[A-Za-z0-9][A-Za-z0-9._-]{0,63}` に一致する。
- `start_cron` と `stop_cron` は両方を指定するか、両方を省略する。
- cron は空白で分割したフィールド数が 5 であり、gronx の検証にも成功する。
- 各 cron は、検証時刻を `DEFAULT_TIMEZONE` へ変換した時刻を基準として、gronx で直前と直後の発火時刻を取得できる。
- `override` は `running`、`stopped`、`disabled` のいずれかである。
- `override_expires_at` は `override` がある場合だけ指定できる。
- `override_expires_at` は指数表記と小数を含まない正の 10 進整数であり、`253402300799` 以下である。
- `override_expires_at` がある場合は schedule も存在する。
- schedule と override の少なくとも一方が存在する。

正式な書き込み経路では、`override_expires_at` が書き込み時刻より後であることも検証する。読み取り時に失効済みであることは正常であり、失効済み override は存在しないものとして扱う。

6 フィールド cron と 7 フィールド cron は、gronx が受理する場合でも拒否する。`0 0 31 2 *` のように構文が正しくても発火しない式は、直前または直後の発火時刻を取得できないため拒否する。

### 更新の原子性

グループアイテムに `revision` は保存しない。設定変更では対象アイテムを強い整合性の `GetItem` で読み取り、厳密に復号し、変更後のグループ全体を検証する。書き込みは、以下に示す操作別の `PutItem`、`UpdateItem`、または `DeleteItem` で行う。

| 操作 | 既存グループ | 未作成グループ | 書き込み時の条件 |
| --- | --- | --- | --- |
| `schedule` | 両 cron だけを `SET` する | schedule を持つアイテムを `PutItem` する | 更新はアイテムの存在、作成は `attribute_not_exists(pk)` |
| 無期限 `override` | `override` を `SET` し、失効時刻を `REMOVE` する | override だけを持つアイテムを `PutItem` する | 更新はアイテムの存在、作成は `attribute_not_exists(pk)` |
| 期限付き `override` | override と失効時刻を `SET` する | 拒否する | アイテムと両 cron の存在 |
| `clear-override` | override と失効時刻を `REMOVE` する | 拒否する | アイテムと両 cron の存在 |
| `remove` | `DeleteItem` する | 対象なしとして返す | アイテムの存在 |

`UpdateItem` は既存アイテムの対象属性だけを変更し、無条件の upsert には使用しない。アイテムの存在と両 cron の存在は、該当する `UpdateItem` の `ConditionExpression` で検査する。読み取り後に `remove` が完了した場合、古い `UpdateItem` は条件不一致となり、グループを再作成しない。[DynamoDB の UpdateItem](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_UpdateItem.html)

schedule と override のように異なる属性を変更する書き込みは互いの値を維持する。同じ属性への同時書き込みは競合を検出せず、DynamoDB が後に適用した値を採用する。条件不一致は競合として返し、自動的に再試行しない。

同じグループ名の `remove` と再作成を、別の設定変更と同時に行うことはサポートしない。`revision` がないため、削除前の読み取りに基づく変更と、削除後に同名で再作成したアイテムを区別できない。並行して行う必要がある場合は新しいグループ名を使用する。

CLI と Web コンソールは同じ application service を使用する。DynamoDB への直接書き込みとグループアイテムの IaC 管理はサポートしない。

### TTL

TTL は設定しない。`override_expires_at` を TTL 属性にすると、override だけでなくグループアイテム全体が削除されるためである。

## 望ましい状態

### 解決順序

グループの望ましい状態は、次の順序で解決する。

1. 有効な override が `disabled` なら、グループを reconcile 対象外とする。
2. 有効な override が `running` なら、望ましい状態を `running` とする。
3. 有効な override が `stopped` なら、望ましい状態を `stopped` とする。
4. schedule の直近の start と stop を比較する。

override は `override_expires_at <= now` の場合に失効済みである。失効済み属性は DynamoDB に残してよい。

### schedule

schedule は `start_cron` と `stop_cron` の組である。`DEFAULT_TIMEZONE` のローカル時刻で直近の発火時刻を求め、新しい側を望ましい状態とする。同時刻なら `stopped` とする。

夏時間による存在しない時刻と重複する時刻は、cron library と組み込み tzdata の動作に従う。グループ単位の例外規則は設けない。

## リソースの所属

### 固定タグ

リソースの所属タグを、以下に示す。

| タグキー | タグ値 |
| --- | --- |
| `cheapskate:group` | グループ名 |

任意の selector とグループ単位の resource types は持たない。同じ AWS account と region で複数の cheapskate installation を動かしてはいけない。

### サイクル単位の一括読み込み

reconcile は AWS リソースを操作する前に、全グループ設定を 1 回の Query 系列で読み込み、グループ名をキーとする map を作る。続いて Resource Groups Tagging API の `GetResources` を 1 系列だけ実行する。filter は、値を指定しない `cheapskate:group` と全対応 resource types である。値を指定しない tag filter は、そのキーを持つ全リソースを返す。[GetResources の TagFilters](https://docs.aws.amazon.com/resourcegroupstagging/latest/APIReference/API_GetResources.html)

全 page の取得後、ARN をキーとする map を作り、返された `cheapskate:group` の値で設定 map へ関連付ける。タグキーは 1 リソースにつき 1 個なので、1 ARN の所属先も 1 グループである。グループごとの検索、永続的な claims、所有権の競合検出、およびグループ間の優先順位は持たない。

ページ取得中のタグ変更などによって同じ ARN が複数回返された場合は、最後に読み取った値で map を上書きする。AWS 操作は全 page の読み込み後に map の各 ARN へ 1 回だけ行うため、同じサイクルで 1 ARN に Start と Stop の両方を送らない。選ばれた group 値が一時的に古くても、次の full reconcile で現在のタグへ収束させる。

設定がないグループ名、不正なグループ名、および空の値はリソース単位の設定エラーとし、AWS の Describe、Start、Stop を行わない。

### 対応リソース

対応範囲を、以下に示す。

| resource type | 対応範囲 | タグ付け対象 |
| --- | --- | --- |
| `rds-instance` | Aurora member と RDS Custom を除く、独立した RDS DB instance | DB instance |
| `rds-cluster` | Aurora DB cluster | DB cluster だけ |
| `ecs-service` | long ARN 形式で scheduling strategy が `REPLICA` の ECS service | ECS service |
| `ec2-instance` | start / stop をサポートする EC2 instance | EC2 instance |

RDS DB instance は Describe 結果の `DBClusterIdentifier` が空であることを検査する。値がある場合は cluster member としてエラーにし、`StopDBInstance` と `StartDBInstance` を呼ばない。Aurora は cluster だけに所属タグを付けること。

RDS Custom は engine 名が `custom-` で始まる場合にエラーとする。`StopDBInstance` の API 仕様が RDS Custom と Aurora を対象外としているためである。[StopDBInstance](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_StopDBInstance.html)

RDS cluster は engine 名が `aurora` で始まる場合だけ処理する。Multi-AZ DB cluster などはエラーとする。`StopDBCluster` は Aurora DB cluster だけを対象とするためである。[StopDBCluster](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_StopDBCluster.html)

AWS API が持つ追加の状態条件は再実装しない。対象範囲内のリソースでも API が拒否した場合は、リソース単位のエラーとして記録する。

ECS service は `DescribeServices` が返す scheduling strategy を検査する。`REPLICA` 以外の場合はリソース単位のエラーとし、Application Auto Scaling と `UpdateService` の変更 API を呼ばない。`DAEMON` は desired count と Service Auto Scaling による台数管理を行わないため、対応範囲外である。[Amazon ECS service の scheduling strategy](https://docs.aws.amazon.com/AmazonECS/latest/developerguide/service_definition_parameters.html)

### ECS 設定タグ

固定タグの制限は所属タグだけを対象とする。ECS の起動規模を指定する既存タグは維持する。

ECS 設定タグを、以下に示す。

| タグキー | 既定値 | 検証 |
| --- | --- | --- |
| `cheapskate/desired-count` | `1` | 正の int32 |
| `cheapskate/scaling-min` | desired count | 0 以上の int32 |
| `cheapskate/scaling-max` | desired count | 0 以上の int32 |

3 値は `scaling-min <= desired-count <= scaling-max` を満たす必要がある。すべての値は scalable target の有無にかかわらず検証する。scaling min と scaling max を AWS API へ適用するのは、scalable target がある場合だけである。

ECS 設定タグは、リソース探索後、Application Auto Scaling または ECS の変更 API を呼ぶ前に検証する。不正な場合はリソース単位のエラーとし、停止処理を含む変更 API を呼ばない。停止後の規模を DynamoDB へ保存しないため、停止前から復元に使用できない設定であることが判明しているサービスは変更しない。

停止時の変更 API を、以下に示す。

| scalable target | 変更 API |
| --- | --- |
| あり | `RegisterScalableTarget(0, 0)` だけを呼ぶ |
| なし | `UpdateService(desiredCount=0)` だけを呼ぶ |

既存の scalable target に `RegisterScalableTarget(0, 0)` を実行すると、現在の capacity も 0 へ変更される。この場合に `UpdateService` と rollback は実行しない。[RegisterScalableTarget](https://docs.aws.amazon.com/autoscaling/application/APIReference/API_RegisterScalableTarget.html)

起動時は、scalable target がある場合に `RegisterScalableTarget` で上下限を desired count のタグ値へ一時的に揃える。台数を再取得し、タグ値と異なる場合だけ `UpdateService` で設定する。成功後に上下限を min / max のタグ値へ戻す。途中で失敗した場合は一時的な上下限を維持し、通常値との差によって次回 reconcile で起動処理を再開する。通常の上下限も desired count と同値の場合は、上下限の設定で起動台数へ調整されるため収束済みとなる。scalable target がない場合、変更 API は `UpdateService` だけを呼ぶ。desired count は起動時の設定値であり、起動後に Service Auto Scaling が変更した値を継続的に元へ戻さない。

## reconcile

### サイクル

reconcile サイクルの処理順を、以下に示す。

1. `CONFIG` partition を強い整合性の Query で読み取る。
2. 全グループを検証し、望ましい状態を解決する。
3. 対応リソースを 1 系列の `GetResources` で取得し、ARN をキーとする map を作る。
4. `disabled` 以外のリソースを Describe する。
5. 安定状態が望ましい状態と異なる場合だけ Start または Stop を実行する。
6. ログ、任意のメトリクス、SNS アクション通知、およびサイクル summary を出力する。

DynamoDB Query の失敗と全体探索の失敗はサイクル全体を中止し、Lambda のエラーとして返す。グループまたはリソース単位の失敗は他のリソースの処理を継続し、summary と構造化ログへ出力する。

Query は `ConsistentRead=true` で読み取る。設定変更の完了後に Query を開始した invocation は、変更後の設定を使用する。設定変更前に Query を開始した invocation だけが古い設定を使用しうる。強い整合性の読み取りは、安全な管理終了手順をリースや revision なしで成立させるために維持する。[DynamoDB の読み取り整合性](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/HowItWorks.ReadConsistency.html)

reconciler が state テーブルへ要求する操作は Query だけである。Status、リース、Scan、および DynamoDB への書き込みを持たない。

### 対応規模

1 サイクルは、すべての設定、すべての `GetResources` page、および対象リソースの Describe と変更を、設定された Lambda timeout 内で完了する必要がある。リソース数による固定上限は設けない。所要時間は AWS API の応答時間、変更が必要なリソース数、および retry にも依存するためである。

`GetResources` の `ResourcesPerPage` は最大 100 であり、Lambda timeout は最大 900 秒である。実運用で想定する最大数のタグ付きリソースを使用し、設定した timeout に余裕を残して完了することをデプロイ前に確認すること。[GetResources](https://docs.aws.amazon.com/resourcegroupstagging/latest/APIReference/API_GetResources.html) [Lambda timeout](https://docs.aws.amazon.com/lambda/latest/dg/configuration-timeout.html)

分割実行と checkpoint は持たない。設定と `GetResources` の全 page を読み終える前に timeout またはエラーが発生した場合、当該サイクルはリソースを変更しない。継続的に 1 サイクルへ収まらない環境は対応範囲外である。

### 多重実行

多重実行を排除しない。予約同時実行数 1 は、重複 API と実行コストを減らしたい場合に設定できる任意の最適化である。

同じ設定 snapshot を読んだ複数サイクルは、同じ Start または Stop を送信しうる。異なる設定 snapshot を読んだサイクルは、相反する操作を送信しうる。running または stopped の管理が継続している限り、古い snapshot を持つ invocation の終了後に成功する reconcile が最新設定へ収束させる。

### 設定変更との競合

設定書き込みは、すでに動作中の reconcile を取り消さない。running、stopped、および schedule の変更は、通常は次回の reconcile で収束する。多重実行中は、古い snapshot を持つ invocation の終了後に成功する reconcile で収束する。

`disabled` と `remove` は望ましい状態をなくす操作であり、変更前のサイクルが開始した AWS 操作を元へ戻さない。両操作は即時停止バリアではない。

特定の状態で安全に管理を終了する手順は次のとおりである。

1. 無期限の `override running` または `override stopped` を設定する。
2. 設定書き込み後、Lambda の設定済み timeout 以上待つ。
3. 手動 reconcile を実行するか、次の定期 reconcile を待つ。
4. `show` で全リソースが指定状態へ収束したことを確認する。
5. リソースから `cheapskate:group` tag を削除する。
6. グループを削除する。

待機により、設定変更前の Query 結果を持つ Lambda invocation は終了する。最終 override の書き込み完了後に開始する強い整合性の Query は、最終 override を読み取る。手順の完了までは対象グループへ別の設定変更を行わないこと。

### AWS 操作の配送保証

Start または Stop の成功後に Lambda が終了した場合、同じ操作を再送しうる。操作回数は at-least-once であり、exactly-once は保証しない。

各サイクルは Describe 結果と望ましい状態を比較する。通常は `transitioning` または収束済みを観測して再送を避ける。AWS の状態反映が遅い場合は、重複 API、invalid state エラー、重複ログ、および重複通知を許容する。

### 呼び出しと収束時間

5 分間隔の定期 full reconcile を唯一の自動呼び出し経路とする。RDS event 専用の呼び出し経路は持たない。手動 invoke は任意の補助経路である。どの呼び出しでも payload の内容は使用せず、全グループと全対応リソースを処理する。

非同期 invoke は throttling、retry exhaustion、または maximum event age によって破棄されうる。1 回の invoke が破棄された場合、次に成功した定期 full reconcile が現在の設定を処理する。通常の追加遅延は 5 分間隔と 1 サイクルの実行時間の和までである。連続した失敗または throttling がある場合の上限は保証しない。[Lambda の非同期再試行](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-error-handling.html)

同期 invoke が throttling された場合、Lambda はキューへ保存しない。呼び出し元が再試行する必要がある。

## Status と doctor

Status item、pending operation、遷移開始時刻、最終アクション、最終エラー、および TTL は保存しない。

doctor、diagnostics 画面、orphan status、stuck transitioning、prune、および selector overlap finding は削除する。不正なグループは通常の設定読み取りで検出する。

## CLI

### コマンド

正式なコマンドを、以下に示す。

| コマンド | 処理 |
| --- | --- |
| `list` | 全グループの schedule と override を出力する |
| `show --group G` | 設定、所属リソース、現在状態を出力する |
| `schedule --group G -start C1 -stop C2` | schedule を作成または置換する |
| `override --group G running\|stopped\|disabled [-for D]` | override を設定する |
| `clear-override --group G` | override を削除して schedule へ戻す |
| `remove --group G` | グループを削除する |

`set-selector`、`pin`、`unpin`、`disable`、`doctor` は削除する。`disable` の意味は無期限の `override disabled` で表す。

`clear-override` は schedule が存在する場合だけ成功する。schedule がないグループの管理を終了する場合は `remove` を使用する。

### エラー出力

CLI の終了コードを、以下に示す。

| 終了コード | 意味 |
| --- | --- |
| 0 | 対象データがすべて有効であり、処理に成功した |
| 1 | 引数、AWS API、DynamoDB、または内部処理のエラー |
| 2 | 読み取ったグループに設定エラーがある、または条件付き書き込みが競合した |

text 形式の `list` は有効なグループを stdout、不正なグループを stderr へ出力し、1 件でも不正なら終了コード 2 とする。

JSON 形式の `list` は stdout に `groups` と `errors` を持つ 1 個の JSON object を出力する。設定エラーがある場合も JSON は完全に出力し、終了コード 2 とする。stderr は JSON の一部として使用しない。

`show` の対象グループが不正な場合はデータを返さず終了コード 2 とする。JSON 形式では stdout に構造化された error object を出力する。

## Web コンソール

Web コンソールはグループ一覧、グループ詳細、schedule form、および override form を提供する。diagnostics、selector、mode、およびグループ単位の timezone は表示しない。

所属タグとして `cheapskate:group=<group>` を表示する。cron と失効日時は `DEFAULT_TIMEZONE` で表示して解釈する。

不正なグループは AWS リソースを探索せず、設定エラーを表示する。条件付き書き込みの競合は HTTP 409 とする。

## 通知とメトリクス

### SNS 通知

SNS は Start または Stop が成功した場合のアクション通知だけを送信する。送信は同一 Lambda 呼び出し内で最大 2 回試行し、失敗しても AWS 操作の結果を変更しない。通知の欠落と重複を許容する。

通知本文は `group`、`resource_id`、`action`、`desired`、`at` を持つ。永続化しない operation ID は使用しない。

### カスタムメトリクス

`METRICS_ENABLED=true` の場合に出力するカスタムメトリクスを、以下に示す。

| メトリクス | 値 |
| --- | --- |
| `ReconciledResources` | Describe まで到達した一意なリソース数 |
| `ReconcileActions` | 成功した Start / Stop 数 |
| `ReconcileErrors` | グループまたはリソース単位のエラー数 |
| `ReconcileAborted` | サイクル全体を中止した場合は 1、それ以外は 0 |

`METRICS_ENABLED=false` の場合は EMF を出力しない。メトリクスが無効でも構造化ログと Lambda の戻り値は維持する。

### AWS リソースの作成

cheapskate は CloudWatch log group、alarm、dashboard、および SNS topic を IaC または AWS API でプロビジョニングしない。`METRICS_ENABLED=true` の場合は EMF を出力するだけである。CloudWatch Logs の保持、alarm、および通知先の構成は規定しない。

## IAM

reconciler に必要な DynamoDB 権限は `dynamodb:Query` だけである。`CONFIG` partition だけを読み取る。

CLI と Web コンソールはグループの `Query`、`GetItem`、`PutItem`、`UpdateItem`、および `DeleteItem` を持つ。`Scan`、`BatchGetItem`、Status 用権限、および TTL 用権限は持たない。

AWS リソースについては、Resource Groups Tagging API、対応 resource type の Describe、Start、Stop、ECS Application Auto Scaling、および任意の SNS Publish を許可する。

## 許容する制約

簡素化後に残る制約を、以下にまとめる。

| 制約 | 判断 |
| --- | --- |
| AWS 操作、ログ、通知の重複 | 後続 reconcile の収束を前提として許容する |
| 設定変更直後の古い snapshot による操作 | 管理を継続する状態では後続 reconcile が修正するため許容する |
| 同じ設定属性への同時書き込み | DynamoDB で後に適用された値を採用するものとして許容する |
| 同名グループの削除と再作成を含む同時書き込み | revision を持たない代わりにサポート対象外とする |
| `disabled` と `remove` が実行中操作を取り消さない | 安全な管理終了手順を必要条件として許容する |
| SNS アクション通知の欠落 | 完全な監査を要件とせず許容する |
| Status による履歴の不存在 | 構造化ログだけで運用できる場合に許容する |
| メトリクス無効時に能動的な障害通知がない | 明示的な設定選択として許容する |
| 連続 throttling 中の収束時間に上限がない | 次の成功サイクルまで遅延するものとして許容する |
| 同じ account と region に複数 installation を置けない | 固定 group tag の簡素さを優先して許容する |
| グループ設定を直接 IaC 管理できない | 一時 override と単一アイテムを優先して許容する |
| RDS Custom と Aurora member を操作しない | 不正な API 呼び出しを避けるため許容する |
| ECS `DAEMON` service を操作しない | desired count による管理対象を `REPLICA` に限定するため許容する |
| ECS 設定タグが不正な場合は停止もしない | 自動起動できない状態を cheapskate が作らないため許容する |
| 未知のグループ属性を拒否する | 属性名の入力ミスを fail-closed にするため許容する |
| full reconcile が Lambda timeout 内に収まらない規模 | 分割実行と checkpoint を持たず、対応範囲外とする |
