# グループ設定

本ドキュメントはグループ名、スケジュール、override、ECS 台数上限、および DynamoDB に直接登録する場合のデータ形式を定義する。

## グループ名

グループ名は 1～64 文字であり、ASCII の英字、数字、ピリオド、アンダースコア、およびハイフンを使用できる。先頭は ASCII の英字または数字とする。リソースの cheapskate:group タグにはこの名前をそのまま設定する。

## スケジュールと override

各グループには、スケジュールまたは override の少なくとも一方が必要である。設定の組み合わせを、以下に示す。

| 設定 | 必要な項目 |
| --- | --- |
| スケジュールのみ | start_cron と stop_cron |
| 無期限 override のみ | override |
| スケジュールと無期限 override | start_cron、stop_cron、override |
| スケジュールと期限付き override | start_cron、stop_cron、override、override_expires_at |

### cron とタイムゾーン

start_cron と stop_cron は、両方を 5 フィールド cron で指定する。過去と未来の発火時刻を取得できる必要があり、探索は各方向 400 年以内で行う。タイムゾーンはグループごとに保存せず、実行時設定 DEFAULT_TIMEZONE を使用する。

夏時間などで現地時刻が繰り返される場合は、各回を発火候補とする。存在しない現地時刻には発火しない。直近の起動・停止は実際の時間軸上で比較する。

### override の値と期限

override の値は running、stopped、disabled のいずれかである。

override_expires_at は Unix time 秒の整数であり、1～253402300799 を指定する。新規保存・更新時は未来の時刻である必要がある。無期限の場合は属性を省略し、0 や NULL を保存しない。

保存済みの override_expires_at が現在時刻以下なら、override は失効済みとして無視する。属性は自動削除されない。CLI または Web コンソールで次の設定変更を行う前に、失効した override を解除するか、新しい override に置き換える。

## ECS 台数上限

ecs_max_count は所属する各 ECS service のタグ値を検証する DynamoDB 属性である。検証対象は起動台数タグ cheapskate:desired-count と通常の上限タグ cheapskate:scaling-max の値とする。グループ全体の合計台数には適用しない。Scheduled Scaling が設定する上下限と現在台数も、この検証を通らない。

値は Number 型の正の int32、1～2147483647 とする。ECS の起動や稼働中の設定更新には登録が必要である。停止にはこの上限を適用しない。

停止だけを行うグループや、RDS、Aurora、および EC2 だけのグループでは省略できる。未使用の場合は 0 や NULL で埋めず、属性を省略する。

不正な属性型や値がある場合はグループ全体の設定エラーとなり、停止も含めて同じグループのリソースを処理しない。

CLI の ecs-max-count で上限を登録・変更し、clear-ecs-max-count で属性を削除する。どちらも既存グループを対象とし、スケジュールと override を保持する。Web コンソールは値を表示し、スケジュールや override の変更時に保持する。上限の登録・変更・削除の詳細は[ECS service の設定](resources/ecs-service.md#dynamodb-と-cli) を参照。

## DynamoDB の登録形式

テーブルのパーティションキーは String 型の pk、ソートキーは String 型の sk とする。1 グループにつき 1 アイテムを保存する。TTL とセカンダリインデックスは使用しない。

リソースごとのアイテムは作成しない。グループ設定の変更はリソースタイプを問わず所属する全リソースに影響する。異なるスケジュールや override が必要な場合は、グループを分ける。

属性を、以下に示す。

| 属性 | DynamoDB の型 | 値 |
| --- | --- | --- |
| pk | String | CONFIG |
| sk | String | GROUP#<グループ名> |
| start_cron | String | 起動時刻の cron |
| stop_cron | String | 停止時刻の cron |
| override | String | running、stopped、disabled |
| override_expires_at | Number | override の有効期限の Unix time 秒 |
| ecs_max_count | Number | ECS service ごとの台数上限 |

未知属性、不正な属性型、および無効な項目の組み合わせは、そのグループの設定エラーとなる。アイテムにリソースの現在状態や操作履歴を追加してはいけない。

平日 09:00～18:00 のスケジュールと ECS 上限 3 を持つ dev グループを、DynamoDB JSON 形式で示す。

```json
{
  "pk": {"S": "CONFIG"},
  "sk": {"S": "GROUP#dev"},
  "start_cron": {"S": "0 9 * * 1-5"},
  "stop_cron": {"S": "0 18 * * 1-5"},
  "ecs_max_count": {"N": "3"}
}
```

直接書き込みでは CLI と Web コンソールの検証を実行しないため、属性型と設定の組み合わせを維持する必要がある。Terraform による登録例と変更時の制約は、[Terraform によるグループ設定](terraform_records.md)を参照。
