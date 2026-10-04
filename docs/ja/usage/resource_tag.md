# リソースタグ

## グループへの所属

管理する各リソースに、次の固定タグを 1 個設定する。

```text
cheapskate:group=<グループ名>
```

reconciler は、値を指定しない `cheapskate:group` tag filter で Resource Groups Tagging API の `GetResources` を呼び出す。1 ページあたり最大 100 リソースを要求し、全ページを読み込む。対応する RDS instance、RDS cluster、ECS service、および EC2 instance の resource type filter も指定する。所属先は、各 ARN とともに返されたタグ値だけで決定する。同じ ARN が複数回返された場合は最後のタグ一式を採用し、1 サイクルに 1 回処理する。ARN の解析に失敗した場合は探索全体が失敗する。ECS service には cluster 名を含む長形式の ARN が必要である。

## EC2 の対象条件

Auto Scaling グループに所属する EC2 instance は管理対象外である。`DescribeInstances` のレスポンスに `aws:autoscaling:groupName` タグが含まれる場合、`cheapskate:group` タグを設定していても、その instance をエラーとして報告し、起動・停止を実行しない。他のリソースの処理は継続する。

AWS は Auto Scaling グループの所属 instance にこのタグを自動付与する。タグのライフサイクルは、[Auto Scaling グループとインスタンスにタグを付ける](https://docs.aws.amazon.com/ja_jp/autoscaling/ec2/userguide/ec2-auto-scaling-tagging.html)を参照。

## ECS の復元設定

ECS には停止状態がないため、cheapskate は service を 0 まで scale in し、起動時に service 自身のタグから値を復元する。使用するタグを次の表に示す。

| タグ | 必須 | 意味 |
| --- | --- | --- |
| `cheapskate/desired-count` | いいえ。既定値 `1` | 起動の初期設定で使用する desired task 数。正の int32 |
| `cheapskate/scaling-min` | いいえ。既定値は desired count | 復元する minimum capacity。0 以上の int32 |
| `cheapskate/scaling-max` | いいえ。既定値は desired count | 復元する maximum capacity。0 以上の int32 |
| `cheapskate/scheduled-scaling-paused` | いいえ。既定値 `false` | Scheduled Scaling の継続的な一時停止要求。小文字の `true` または `false` |

Start には、所属グループの登録レコードに Number 型の `ecs_max_count` が必要である。タグの既定値を適用した後の desired count と scaling-max がこの上限を超える場合、更新 API を呼ばずエラーとなる。上限が未登録の場合も Start はエラーとなる。Stop にはこの上限を適用しない。上限の登録手順は、[操作方法の ECS 台数上限](operations.md#ecs-台数上限)を参照。

scalable target の有無によらず、3 個の台数設定が `0 <= min <= desired <= max` を満たす必要がある。タグが存在しない場合だけ既定値を使い、空文字列はエラーにする。一時停止要求も target の有無によらず検証し、`true` と `false` 以外の値はエラーにする。

ECS または Application Auto Scaling の変更 API を呼び出す前に、タグ一式を検証する。`REPLICA` 以外の scheduling strategy を使用する service はエラーになる。

### Scheduled Scaling の一時停止

`cheapskate/scheduled-scaling-paused=true` は、ECS service の稼働中も Scheduled Scaling の一時停止を維持する要求である。cheapskate はタグを書き換えず、ECS service の起動、スケジュールの発火、および override の変更によって要求を解除しない。要求の解除はタグを `false` に変更するか削除する操作で行う。

AWS の `ScheduledScalingSuspended` は、タグの要求と ECS service への停止要求から決定する。管理中の安定状態で適用する値を、以下に示す。

| グループの望ましい状態 | 一時停止要求のタグ | `ScheduledScalingSuspended` |
| --- | --- | --- |
| `stopped` | 任意 | `true` |
| `running` | `true` | `true` |
| `running` | `false` または未設定 | `false` |
| `disabled` | 任意 | 変更しない |

ECS service の起動設定の途中でも AWS フラグを `true` に維持する。起動設定が完了した最後の更新でタグの値を適用する。タグが `false` でも、cheapskate による ECS service の停止要求が有効な間は Scheduled Scaling を一時停止する。

scheduled action の定義は維持する。`DynamicScalingInSuspended` と `DynamicScalingOutSuspended` は変更しない。AWS フラグを直接変更しても新しい要求として取り込まず、次の正常な reconcile でタグとグループの要求を再適用する。AWS の各フラグの対象は、[SuspendedState](https://docs.aws.amazon.com/autoscaling/application/APIReference/API_SuspendedState.html)を参照。

### 起動・停止と上下限

scalable target がある場合、Stop は上下限 `0/0` と `ScheduledScalingSuspended=true` を同じリクエストで適用する。タスク数がすべて 0、上下限が `0/0`、または起動途中の固定範囲が残る場合、Start は起動の初期設定を行う。上下限を desired count のタグ値へ固定して Scheduled Scaling を一時停止し、台数を再取得してタグ値と異なる場合だけ更新する。設定に成功した後、上下限を通常のタグ値へ戻し、一時停止要求を適用する。

稼働中の上下限の管理範囲を、以下に示す。

| 一時停止要求のタグ | 上下限の扱い |
| --- | --- |
| `true` | scaling-min と scaling-max のタグ値を維持する |
| `false` または未設定 | 起動の初期設定後は Scheduled Scaling による変更を許可する |

一時停止要求が `false` の間は、上下限とタグの差だけを理由に Start を再適用しない。通常のタグ値を継続して維持する場合は、Scheduled Scaling を使用しない service も含めて `true` を指定すること。

稼働中に `false` から `true` へ変更すると、上下限もタグ値へ戻す。現在の desired count がその範囲外の場合、AWS が台数を調整しうる。`true` から `false` への変更では、現在の上下限を維持して AWS フラグだけを解除する。いずれの場合も、起動台数への再設定は行わない。フラグだけの更新でも範囲外の capacity を調整する API の動作は、[RegisterScalableTarget](https://docs.aws.amazon.com/autoscaling/application/APIReference/API_RegisterScalableTarget.html)を参照。

scalable target がない場合、Stop は desired count を 0 にし、Start は設定値または既定値へ戻す。Scheduled Scaling を制御するための target は作成しない。後から target が作成された場合は、次回 reconcile で一時停止要求を適用する。

### 状態判定と復旧

ECS の停止判定は desired count、running count、pending count がすべて 0 であることを条件とする。それ以外は稼働状態とする。停止要求に対して、上下限が `0/0` でない場合または AWS フラグが `false` の場合は Stop を再適用する。稼働要求に対して、起動途中の設定、一時停止要求と AWS フラグの差、またはタグが `true` の場合の上下限の差があれば Start を再適用する。稼働中の desired count と起動台数タグとの差だけでは Start を実行しない。

起動途中に失敗した場合は、その時点の AWS 設定を残す。通常のタグ範囲と異なる、一時停止中の正の固定範囲を初期設定の途中として扱い、現在のタグ値から復旧する。通常の上下限も起動台数と同じ場合は、一時停止フラグの差だけを再適用できる。最後の更新の応答だけが失われ、既に設定が一致している場合は追加操作を行わない。通知は欠落しうる。

グループの望ましい状態が `running` の間に Scheduled Scaling がタスク数をすべて 0 にした場合も、次回 reconcile で起動する。0 台を維持する時間帯は、cheapskate のスケジュールまたは `stopped` override で指定すること。

一時停止の設定反映と管理終了の手順は、[操作方法](operations.md#ecs-scheduled-scaling-の一時停止)を参照。
