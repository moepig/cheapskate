# リソースタグ

本ドキュメントは、管理対象の所属タグと ECS の設定タグについて、設定先、既定値、および値の制約を示す。

## 所属タグ

管理する各 AWS リソースに、所属先のグループ名を指定する。設定するタグは次のとおりである。

```text
cheapskate:group=<グループ名>
```

タグ値と同名のグループ設定を DynamoDB に登録する必要がある。空の値、不正なグループ名、または未登録のグループ名は、そのリソースのエラーとなる。グループ名の制約は、[グループ設定](group_config.md#グループ名)を参照。

reconciler は Resource Groups Tagging API で、cheapskate:group キーを持つ対応リソースを検出する。所属先は、ARN とともに返されたタグ値で決まる。ECS service には cluster 名を含む長形式の ARN が必要である。

EC2 の aws:autoscaling:groupName タグは AWS が Auto Scaling グループの所属インスタンスに付与する。これを持つインスタンスは cheapskate の管理対象外である。対象条件は、[起動・停止の挙動](resource_flow.md#対応リソースと操作の意味)を参照。

## ECS 設定タグ

ECS service 自身に設定するタグを、以下に示す。グループ設定の ecs_max_count は DynamoDB に別途登録する。

| タグ | 既定値 | 意味 | 制約 |
| --- | --- | --- | --- |
| cheapskate/desired-count | 1 | 停止状態または上下限 0/0 から起動する際の台数 | 正の int32 |
| cheapskate/scaling-min | cheapskate/desired-count の値 | 通常の MinCapacity | 0 以上の int32 |
| cheapskate/scaling-max | cheapskate/desired-count の値 | 通常の MaxCapacity | 0 以上の int32 |
| cheapskate/scheduled-scaling-paused | false | 稼働中も Scheduled Scaling を一時停止する要求 | 小文字の true または false |

既定値はタグが存在しない場合だけ適用する。空文字列はエラーである。台数は、既定値の適用後に cheapskate/scaling-min <= cheapskate/desired-count <= cheapskate/scaling-max を満たす必要がある。

scalable target の有無によらず、起動・停止前にタグ一式を検証する。不正なタグがある場合、その service を変更しない。起動や稼働中の設定更新には、cheapskate/desired-count と cheapskate/scaling-max がグループの ecs_max_count 以下であることも必要である。

タグの適用タイミングとスケーリングの挙動は、[ECS の起動・停止とスケーリング](ecs.md)を参照。台数上限の登録と一時停止の操作は、[日常の操作](operations.md#ecs-の設定変更)を参照。
