# CLI によるグループ設定例

本ドキュメントは、平日の起動・停止、無期限 override、および期限付き override について、CLI の操作と DynamoDB に保存される設定を具体例で示す。

## 前提

以下の例では、既存の DynamoDB テーブル cheapskate と、5 分間隔で実行する reconciler を使用する。CLI と reconciler の DEFAULT_TIMEZONE は、ともに Asia/Tokyo とする。テーブル、Lambda、および IAM の準備は、[セットアップ](setup.md)を参照。

CLI の接続先とタイムゾーンの設定を、以下に示す。

```sh
export AWS_REGION=ap-northeast-1
export CHEAPSKATE_TABLE=cheapskate
export DEFAULT_TIMEZONE=Asia/Tokyo
```

DynamoDB は 1 グループにつき 1 アイテムを保存する。キーは pk=CONFIG、sk=GROUP#<グループ名> である。CLI は設定を書き込み、reconciler が後続サイクルでその設定と現在状態を比較して起動・停止する。リソースには所属タグを別途設定する。

ECS の起動には、所属グループのアイテムに Number 型の ecs_max_count を登録する必要がある。cheapskate-cli ecs-max-count --group NAME COUNT で登録する。以下の起動動作は、ECS では desired count と scaling-max がその上限内にある場合に実行される。上限の登録手順は、[ECS 台数上限の登録](operations.md#ecs-台数上限の登録)を参照。

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

- DynamoDB に dev の 1 アイテムを作成する。同名グループがあれば 2 個の cron 属性だけを更新する。
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
- unmanaged：sk=GROUP#unmanaged と String 型の override=disabled を保存する。所属リソースの状態確認と起動・停止を行わない。

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
- 失効しても DynamoDB の属性は残る。DynamoDB TTL による削除は行わない。

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
> override だけのグループでは clear-override を使用できない。先にスケジュールを追加するか、別の override で置き換える。グループ自体の削除は、[管理の終了](operations.md#管理の終了)を参照。

## 設定の反映確認

CLI の成功は設定の保存完了を表す。AWS の起動・停止完了は、show の現在状態で確認する。反映タイミングと対応リソースは、[起動・停止の挙動](resource_flow.md)を参照。ECS の台数とスケーリングの具体例は、[ECS の起動・停止とスケーリング](ecs.md#設定と現在状態の具体例)を参照。
