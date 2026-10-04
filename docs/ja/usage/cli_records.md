# CLI によるレコード登録と AWS の動作

本ドキュメントは、CLI によるグループ設定の登録例、DynamoDB に保存されるレコード、および登録後の AWS リソースの動作を示す。

## 前提

以下の例では、既存の DynamoDB テーブル cheapskate と、5 分間隔で実行する reconciler を使用する。CLI と reconciler の DEFAULT_TIMEZONE は、ともに Asia/Tokyo とする。テーブル、Lambda、および IAM の準備は、[セットアップ](setup.md)を参照。

CLI の接続先とタイムゾーンの設定を、以下に示す。

```sh
export AWS_REGION=ap-northeast-1
export CHEAPSKATE_TABLE=cheapskate
export DEFAULT_TIMEZONE=Asia/Tokyo
```

登録から反映までの共通動作は、次のとおりである。

- DB は 1 グループにつき 1 アイテムを保存する。キーは pk=CONFIG、sk=GROUP#<グループ名> である。
- CLI の登録コマンドは設定を書き込む。AWS リソースの作成、タグ付け、起動・停止は行わない。
- 各リソースには、所属先を示す cheapskate:group=<グループ名> タグを別途設定する。
- reconciler は全グループとタグ付きリソースを読み、その時点の設定に従って必要な起動・停止操作を行う。
- リソースごとの DB レコードや、グループごとの EventBridge schedule は作成しない。

ECS の起動には、所属グループのアイテムに Number 型の ecs_max_count を別途登録する必要がある。以下の起動動作は、ECS では desired count と scaling-max がその上限内にある場合に実行される。上限の登録手順は、[操作方法の ECS 台数上限](operations.md#ecs-台数上限)を参照。

## 平日の起動・停止

未登録の dev グループを、平日 09:00 から 18:00 まで起動状態にするコマンドを、以下に示す。

```sh
cheapskate-cli schedule --group dev \
  -start '0 9 * * 1-5' -stop '0 18 * * 1-5'
```

登録後のアイテムを DynamoDB JSON 形式で示す。S は String 型を表す。

```json
{
  "pk": {"S": "CONFIG"},
  "sk": {"S": "GROUP#dev"},
  "start_cron": {"S": "0 9 * * 1-5"},
  "stop_cron": {"S": "0 18 * * 1-5"}
}
```

この登録による動作は、次のとおりである。

- DB に dev の 1 アイテムを作成する。同名グループがあれば 2 個の cron 属性だけを更新する。
- cheapskate:group=dev が付いたリソースを、平日 09:00 以降のサイクルで起動する。
- 平日 18:00 以降のサイクルで停止する。金曜日の停止後は月曜日の起動時刻まで停止状態を維持する。
- 登録時点が平日 10:00 なら、その設定を読んだサイクルで起動する。翌日の cron 時刻まで待たない。

## 無期限の override

override は schedule より優先する状態指定である。未登録の 3 グループに、それぞれ異なる override を登録する例を示す。

```sh
cheapskate-cli override --group always-on running
cheapskate-cli override --group always-off stopped
cheapskate-cli override --group unmanaged disabled
```

各アイテムの pk は CONFIG であり、保存内容と動作は次のとおりである。

- always-on：sk=GROUP#always-on と String 型の override=running を保存する。所属リソースを起動状態に保つ。
- always-off：sk=GROUP#always-off と String 型の override=stopped を保存する。所属リソースを停止状態に保つ。
- unmanaged：sk=GROUP#unmanaged と String 型の override=disabled を保存する。reconciler は所属リソースの Describe と起動・停止を行わない。

無期限の場合、override_expires_at は保存しない。既存グループに設定すると schedule は維持され、既存の有効期限は削除される。disabled 自体はリソースの状態を変更しない。

## 期限付きの override

schedule 登録済みの dev を、2026-09-14 の 17:00 に同日 20:00 まで起動状態にする例を示す。期限は実際の実行時刻から計算される。

```sh
cheapskate-cli override --group dev running -for 3h
```

17:00:00 に実行した場合のアイテムを、以下に示す。N は Number 型であり、有効期限は Unix time 秒で保存する。

```json
{
  "pk": {"S": "CONFIG"},
  "sk": {"S": "GROUP#dev"},
  "start_cron": {"S": "0 9 * * 1-5"},
  "stop_cron": {"S": "0 18 * * 1-5"},
  "override": {"S": "running"},
  "override_expires_at": {"N": "1789383600"}
}
```

この更新による動作は、次のとおりである。

- 同じ dev アイテムに override と override_expires_at を追加する。アイテム数は増えない。
- 18:00 の停止時刻を過ぎても、有効な running override に従って起動状態を維持する。
- 20:00 以降のサイクルでは override を無視し、schedule に従って停止する。
- 失効しても DB の属性は残る。DynamoDB TTL による削除は行わない。

期限付き override には schedule が必要である。stopped と disabled にも同様に期限を指定できる。

## override の解除と確認

dev の override を期限前に解除し、現在状態を確認するコマンドを、以下に示す。

```sh
cheapskate-cli clear-override --group dev
cheapskate-cli -output json show --group dev
```

- clear-override は override と override_expires_at を削除し、cron 属性を維持する。次に設定を読んだサイクルから schedule に従う。
- show は設定と AWS の現在状態を読み取る。起動・停止を実行せず、状態が変わるまで待機もしない。

> [!NOTE]
> override だけのグループでは clear-override を使用できない。先に schedule を追加するか、別の override で置き換える。グループ自体の削除は、[操作方法の管理終了手順](operations.md#安全に管理を終了する手順)を参照。

## AWS リソースごとの起動・停止

同じグループに所属するリソースでも、起動・停止の操作は種別によって異なる。望ましい状態への変更が必要な場合の動作を、以下に示す。

- EC2 instance：StartInstances / StopInstances を呼び出す。
- RDS DB instance：StartDBInstance / StopDBInstance を呼び出す。
- Aurora DB cluster：StartDBCluster / StopDBCluster を呼び出す。
- ECS service に scalable target がない場合：起動時は desired count をタグの値へ戻し、停止時は 0 にする。タグ未設定時の起動台数は 1 である。
- ECS service に scalable target がある場合：停止時は min/max を 0/0 にする。起動時は上下限を起動台数に揃え、必要なら desired count を更新し、最後に上下限を復元タグの値へ戻す。

ECS で desired-count=2、scaling-min=1、scaling-max=4 を指定した場合、起動時は min/max を 2/2 にしてから 1/4 へ戻す。タグには cheapskate/ 接頭辞を付ける。タグの設定と ECS の再適用条件は、[リソースタグ](resource_tag.md)を参照。

RDS と EC2 は遷移中なら次のサイクルへ処理を見送る。CLI の成功は設定保存の完了であり、AWS の起動・停止完了を表さない。対応リソースの制約は、[基本概念](concepts.md#対応リソース)を参照。
