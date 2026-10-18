# ECS の設定と現在状態の具体例

本ドキュメントは ECS の設定値と AWS の観測状態を対応づけた 5 つの例を示す。起動・停止とスケーリングの仕様は、[ECS の起動・停止とスケーリング](ecs.md)を参照。

<a id="設定と現在状態の具体例"></a>

## 共通の前提

以下の例は、cluster が dev、service が api で、scalable target が登録済みの場合である。

DynamoDB の ecs_max_count は Number 型、他の属性とタグ値は String 型とする。override_expires_at は登録せず、override は無期限とする。停止の例では override を stopped に変更する。グループアイテムに登録する属性値を、以下に示す。

| DynamoDB の属性 | 登録値 |
| --- | --- |
| pk | CONFIG |
| sk | GROUP#dev |
| override | running |
| ecs_max_count | 3 |

個別に変更を示す例を除き、ECS service に登録するタグ値は次のとおりである。

| ECS service のタグ | 登録値 |
| --- | --- |
| cheapskate:group | dev |
| cheapskate:desired-count | 2 |
| cheapskate:scaling-min | 1 |
| cheapskate:scaling-max | 3 |
| cheapskate:scaling-bounds-mode | scheduled |

以下の状態表は各時点で AWS から取得しうる観測例である。

## 停止状態からの起動と途中失敗

DynamoDB の override=running により起動要求を選択する。ECS の全台数と AWS の上下限が 0 なので、MinCapacity=2、MaxCapacity=2、ScheduledScalingSuspended=true を AWS に適用する。台数固定後の状態取得が失敗し、通常の上下限を適用する前に処理を終了した場合の観測例を、以下に示す。

| 時点 | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | 実際の ECS 状況 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 起動前 | 0 | 0 | 0 | 0 | 0 | true | タスクがなく、停止設定が残っている |
| 台数固定の適用後に失敗 | 2 | 0 | 2 | 2 | 2 | true | 2 台が起動待ちで、稼働タスクはまだない |
| 次のサイクルで通常設定を適用 | 2 | 1 | 1 | 1 | 3 | false | 1 台が稼働し、残り 1 台が起動待ちである |
| タスク起動後 | 2 | 2 | 0 | 1 | 3 | false | 2 台が稼働している |

次のサイクルでは desiredCount が正で上下限も 0/0 ではないため、起動台数への固定は繰り返さない。AWS の設定に MinCapacity=1、MaxCapacity=3、および ScheduledScalingSuspended=false を適用し、起動台数への再設定は行わない。通常設定の適用時にタスクがすべて起動済みである必要はない。

## 稼働中の固定範囲からのタグ変更

DynamoDB の登録値は override=running、ecs_max_count=3 とする。ECS service の所属タグは cheapskate:group=dev のままとし、他のタグを次のように変更する。

| ECS service のタグ | 変更前 | 変更後 |
| --- | --- | --- |
| cheapskate:desired-count | 2 | 1 |
| cheapskate:scaling-min | 2 | 1 |
| cheapskate:scaling-max | 2 | 3 |
| cheapskate:scaling-bounds-mode | tags | tags |

AWS の設定に MinCapacity=1、MaxCapacity=3、および ScheduledScalingSuspended=true を適用する。AWS の現在台数を維持して上下限を変更する観測例を、以下に示す。

| 時点 | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | 実際の ECS 状況 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| タグ変更後、設定適用前 | 2 | 2 | 0 | 2 | 2 | true | 固定範囲で 2 台が稼働している |
| 通常の上下限を適用後 | 2 | 2 | 0 | 1 | 3 | true | 稼働中の 2 台を維持している |

AWS のタスク数はすべて 0 の状態ではなく、上下限も 0/0 ではない。そのため cheapskate:desired-count=1 を適用する起動処理には入らない。現在の desiredCount=2 は変更後の通常範囲 1..3 に収まるため、台数変更を行わない。これと同じ AWS の状態が起動途中の失敗で残った場合にも、同じ操作を適用する。

## 一時停止解除時の範囲外台数

DynamoDB の登録値は override=running、ecs_max_count=3 とする。ECS service のタグは共通設定とし、cheapskate:scaling-bounds-mode=scheduled を指定する。AWS の設定に MinCapacity=1、MaxCapacity=3、および ScheduledScalingSuspended=false を適用する。AWS には以前の上下限 4..6 と一時停止が残り、5 台が稼働している場合の観測例を、以下に示す。

| 時点 | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | 実際の ECS 状況 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 一時停止解除前 | 5 | 5 | 0 | 4 | 6 | true | 5 台が稼働している |
| 通常範囲と解除を適用後 | 3 | 5 | 0 | 1 | 3 | false | 設定台数は 3 台となり、余剰タスクの終了前には 5 台が稼働している |
| タスク終了後 | 3 | 3 | 0 | 1 | 3 | false | 3 台が稼働している |

cheapskate:scaling-min=1 と cheapskate:scaling-max=3 を適用するため、AWS が設定台数を範囲内の 3 台へ調整する。cheapskate:desired-count=2 への再設定は行わない。

## 最後の応答消失後の Scheduled Scaling

DynamoDB とタグは共通設定とする。AWS への MinCapacity=1、MaxCapacity=3、および ScheduledScalingSuspended=false の適用は成功したが、その応答だけが失われた場合を示す。その後、AWS の scheduled action が上下限を 4..6 に変更し、台数が増えた場合の観測例を、以下に示す。

| 時点 | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | 実際の ECS 状況 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 最後の更新が成功し、応答が消失 | 2 | 2 | 0 | 1 | 3 | false | 2 台が稼働し、Scheduled Scaling が有効である |
| scheduled action による変更後 | 4 | 2 | 2 | 4 | 6 | false | 設定台数が下限の 4 台へ増え、2 台が起動待ちである |
| 次のサイクル | 4 | 4 | 0 | 4 | 6 | false | 4 台が稼働している |

次のサイクルでは、上下限管理モードが scheduled で AWS の ScheduledScalingSuspended=false のため、設定を変更しない。タグの通常範囲 1..3 や起動台数 2 へ戻さない。DynamoDB の ecs_max_count=3 は cheapskate が適用する起動台数とタグの上限の検証値であり、scheduled action の上下限や現在台数を制限する値ではない。

## 起動途中からの停止要求

DynamoDB のグループアイテムの override を running から stopped に変更する。ecs_max_count=3 と ECS service の共通タグは変更しない。AWS の設定に MinCapacity=0、MaxCapacity=0、および ScheduledScalingSuspended=true を適用する。起動待ちのタスクがある状態から停止する観測例を、以下に示す。

| 時点 | desiredCount | runningCount | pendingCount | MinCapacity | MaxCapacity | ScheduledScalingSuspended | 実際の ECS 状況 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 停止要求の適用前 | 2 | 1 | 1 | 2 | 2 | true | 1 台が稼働し、1 台が起動待ちである |
| 停止設定の適用後 | 0 | 1 | 0 | 0 | 0 | true | 起動待ちタスクは取り消され、稼働タスクの終了待ちである |
| タスク終了後 | 0 | 0 | 0 | 0 | 0 | true | 稼働中と起動待ちのタスクがなく、停止している |

停止要求はタグの起動台数 2 と上下限管理モード scheduled より優先される。通常の上下限 1..3 を適用する起動復旧は行わない。DynamoDB の override を disabled に変更した場合は AWS の状態を取得せず、変更もしない。その時点の AWS 設定を残す。
