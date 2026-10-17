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

起動台数、通常の上下限、および Scheduled Scaling の一時停止要求は ECS service 自身のタグに設定する。タグ一覧、既定値、制約、および設定コマンドは、[ECS service のタグ側の設定](resources/ecs-service.md#タグ側の設定) を参照。

タグの適用タイミングとスケーリングの挙動は、[ECS の起動・停止とスケーリング](ecs.md) を参照。
