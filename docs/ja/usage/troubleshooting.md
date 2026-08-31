# 障害対応と状態の復旧

本ドキュメントは、reconcile が途中で失敗した場合、リソースが中途半端な状態で停止した場合、および state テーブルに不要なレコードが残った場合の対応手順を扱う。

cheapskate は収束ループであり、失敗しても次のサイクルが同じ処理をやり直す。一過性の失敗の大半は、介入なしに解消する。本ドキュメントが対象とするのは、介入なしには解消しない事象と、その識別方法である。

## 障害の検知

障害を検知する経路と、そこから得られる情報を、以下にまとめる。

| 経路 | 得られる情報 |
| --- | --- |
| SNS 通知 | アクションの実行・失敗・復旧。同一エラーの継続中は再通知されない |
| Lambda `Errors` メトリクス | サイクル全体の異常(payload 不正、初期 Query / BatchGetItem 失敗、タイムアウト、panic) |
| `ReconcileErrors` / `ReconcileActions` / `ReconciledResources` / `ReconcileAborted` メトリクス | 明示的に有効化した場合の件数の推移 |
| `status#` の `last_error` | リソースごとの直近エラー。`cheapskate-cli list` / `show`、Web コンソールで参照する |
| `cheapskate-cli doctor` | state テーブルの不整合と残存レコード |
| CloudWatch Logs | 上記のいずれにも現れない失敗 |

`Errors` はサイクル全体の中断だけを捕捉する。リソース単位・グループ単位の失敗があっても Lambda は成功を返すため、EventBridge によるフル reconcile の再実行は発生しない。これらの失敗は SNS、Status／ログの監視、または明示的に有効化した `ReconcileErrors` で検知する。

> [!WARNING]
> SNS トピック、Status／ログの監視、有効化した `ReconcileErrors` アラームのいずれも無い場合、全リソースが失敗し続けても能動的な検知経路が存在しない。少なくとも 1 つを用意すること。

### メトリクスにも通知にも現れない失敗

SNS Publish の失敗はログに残り、同じ処理内で 1 回だけ再送される。2 回とも失敗した通知は欠落する。終わらない遷移はメトリクスにも通知にも現れないため、`doctor` で捕捉する。

### アラームの設定

次のアラームは、サイクル全体の異常を捕捉する。

```console
aws cloudwatch put-metric-alarm --alarm-name cheapskate-reconciler-errors \
  --namespace AWS/Lambda --metric-name Errors --statistic Sum \
  --dimensions Name=FunctionName,Value=cheapskate-reconciler \
  --period 300 --evaluation-periods 2 --threshold 0 --comparison-operator GreaterThanThreshold \
  --treat-missing-data notBreaching --alarm-actions <SNS トピック ARN>
```

`evaluation-periods` を 2 としているのは、一過性の基盤障害を即時に発報しないためである。1 サイクルで発報させると、介入を要しない事象まで通知対象となる。

リソース単位・グループ単位の失敗件数を観測する場合は `METRICS_ENABLED=true` を設定し、次のアラームを追加する。

```console
aws cloudwatch put-metric-alarm --alarm-name cheapskate-reconcile-errors \
  --namespace cheapskate --metric-name ReconcileErrors --statistic Maximum \
  --period 300 --evaluation-periods 2 --threshold 0 --comparison-operator GreaterThanThreshold \
  --treat-missing-data notBreaching --alarm-actions <SNS トピック ARN>
```

`ReconciledResources`、`ReconcileActions`、`ReconcileErrors` は、サイクルが立ち上がらなかった場合にデータポイント自体が存在しない。この 3 つにアラームを設定する場合は `treatMissingData=notBreaching` とする。

呼び出し自体の停止を検知する場合は、`ReconcileAborted` に `treatMissingData=breaching` のアラームを設定する。呼び出しが停止すればデータポイントが途切れるため、定期実行トリガーの停止やイベントルールの障害を検知できる。

## 未完了処理の残存物

処理が途中で終了した場合に残る事象と、その対応を、以下にまとめる。

| 事象 | 自動解消の可否 | 必要な対応 |
| --- | --- | --- |
| Lambda がタイムアウトしてグループの途中で終了した | 解消する。次サイクルが最初からやり直す | 毎回発生する場合はメモリ/タイムアウトを引き上げる |
| 初期 Query / BatchGetItem に失敗してサイクルが立ち上がらなかった | 解消する。EventBridge の再試行と次サイクルがある | なし |
| Stop/Start が失敗した | 解消する。次サイクルが再試行する | 権限不足など恒久的な原因の場合は `last_error` を確認して解消する |
| pending の保存に失敗した | AWS 操作を実行しないため、次サイクルで再試行する | DynamoDB 障害が続く場合だけ原因を解消する |
| アクションは成功したが完了 Status の書き込みに失敗した | pending を残す。次サイクルが AWS の状態から完了を確定し、同じアクションを再送しない | なし |
| 通知の Publish が 2 回失敗した | 解消しない。通知の送信を打ち切り、AWS 操作は続行する | 必要に応じて `*-notify-abandoned` ログを監視する |
| ECS の停止が desiredCount 更新の手前で失敗した | 解消する。スケーラブルターゲットは元の min/max へ自動で巻き戻る | なし。巻き戻しにも失敗した場合のみ [ECS サービスに固有の事項](#ecs-サービスに固有の事項) |
| リソースが遷移中のまま停止した | 解消しない。毎サイクル skip され続ける | [遷移中のまま停止したリソース](#遷移中のまま停止したリソース) |
| グループを削除したが `override#` / `status#` が残った | 解消しない | `doctor --prune` |
| タグを外したリソースの `status#` が残った | 解消しない(動作への影響はない) | `doctor --prune` |
| セレクタが重複して片方のグループが無視されている | 解消しない | [セレクタの重複](#セレクタの重複) |
| ECS サービスを停止中に管理から外した | 解消しない。desiredCount 0 / Auto Scaling 0-0 のまま残る | [ECS サービスに固有の事項](#ecs-サービスに固有の事項) |

## doctor による診断

state テーブルの不整合と、未完了処理が残したレコードを 1 コマンドで洗い出す。既定では読み取りのみを行う。

```console
cheapskate-cli doctor                      # 診断のみ
cheapskate-cli doctor --prune              # 孤立レコードを削除する
cheapskate-cli doctor --stuck-after 2h     # 遷移中とみなす上限を変える(既定 30m)
```

Web コンソールでは、diagnostics ページが同一の診断結果を表示し、同一の条件で孤立レコードを削除する。

報告される `kind` と、`--prune` の対象かどうかを、以下にまとめる。

| `kind` | 意味 | `--prune` による削除 |
| --- | --- | --- |
| `orphan-override` | `group#` がないのに `override#` が残っている | 削除する |
| `orphan-group-status` | `group#` がないのに `status#group#` が残っている | 削除する |
| `orphan-status` | どのグループのセレクタにも一致しないリソースの `status#` | 削除する |
| `corrupt-record` | 読み取りまたは検証に失敗するレコード | 削除しない |
| `config-error` | 登録済みだが reconciler が従えない設定(pinned なのに desired がない等) | 削除しない |
| `discover-error` | セレクタは妥当だがリソースの検出が失敗した | 削除しない |
| `selector-overlap` | 複数グループのセレクタが同じリソースに一致している | 削除しない |
| `stuck-transitioning` | `--stuck-after` を超えて遷移中のまま | 削除しない |

`--prune` の削除対象は、そのグループやリソースが存在しないことがテーブルの読み取りと検出のみで確定するレコードに限られる。設定そのもの(`group#`)、人間の判断を要する項目、および AWS リソースには触れない。

安全装置として、検出が 1 つでも失敗したサイクルでは `orphan-status` の判定そのものを見送る。一時的に検出できなかっただけのリソースの監査記録を削除しないためである。この場合、`blocked` に理由が入る。

```console
$ cheapskate-cli doctor | jq '{blocked, counts}'
{
  "blocked": ["group \"dev\" could not be discovered (AccessDenied); its members are unknown"],
  "counts": {"discover-error": 1}
}
```

> [!IMPORTANT]
> `blocked` が空でないときの `orphan-status` 0 件は、孤立レコードが存在しないことではなく、判定を行っていないことを意味する。原因を解消したうえで再実行すること。

各項目の `pk` と `sk` に生の DynamoDB キーが入っているため、`--prune` を使わず手動で削除することもできる。

```console
aws dynamodb delete-item --table-name <state-テーブル名> \
  --key "$(cheapskate-cli doctor | jq -c '{pk: {S: .findings[0].pk}, sk: {S: .findings[0].sk}}')"
```

## 緊急時の手順

介入の反映は、いずれも次の reconcile サイクル(既定 5 分)を待つ。待てない場合は reconciler を手動で呼び出す。

```console
aws lambda invoke --function-name cheapskate-reconciler --payload '{}' /dev/stdout
```

`{}` はフル reconcile を意味する。応答の JSON が、そのサイクルの `actions` と `errors` である。

### 全リソースの一括起動

グループ内の全リソースを一時的に起動させるには、期限付きの override を登録する。

```console
cheapskate-cli override --group dev running -for 8h
```

> [!IMPORTANT]
> 操作の順序に制約がある。`disable` したグループには override を登録できない。`disabled` は override より強い停止であるためである。障害対応で先に `disable` を実行した場合、起動させるには先に `pin` または `schedule` へ戻す必要がある。

`disable` からの復帰は、モードを戻すことで行う。

```console
cheapskate-cli pin --group dev running     # disable を実行済みの場合
```

逆に、cheapskate に操作させずに人手で作業する場合は `disable` が適切である。cheapskate はそのグループを一切参照しなくなるが、すでに実行したアクションは巻き戻さない。停止済みのリソースは停止したまま残る。

### グループの削除

グループを管理対象から外すには、設定レコードを削除する。

```console
cheapskate-cli remove --group dev
```

`override#` → `status#group#` → `group#` の順に削除するため、途中で失敗してもグループ本体が残り、再試行で到達できる。リソース単位の `status#` は残るため、削除する場合は `doctor --prune` を用いる。

削除は AWS リソースに一切触れない。削除後もリソースは cheapskate が最後に置いた状態のまま残る。

> [!CAUTION]
> 停止させたまま管理を外す意図がない場合は、`remove` の前に `override running` で起動させ、サイクルを 1 回待つこと。

## ECS サービスに固有の事項

停止は、Application Auto Scaling の min/max を 0/0 にする段階と、desiredCount を 0 にする段階の 2 段階であり、原子的ではない。

後段が失敗した場合、スケーラブルターゲットは元の min/max へ自動で巻き戻る。サービスは起動したままであり、スケールアウト可能な状態が保たれる。

巻き戻しにも失敗した場合に限り、`last_error` に `left clamped at 0/0` と復元すべき値が記録される。この状態のサービスは起動していてもスケールアウトできないため、手動で復元すること。

```console
aws application-autoscaling register-scalable-target --service-namespace ecs \
  --resource-id service/dev-cluster/api --scalable-dimension ecs:service:DesiredCount \
  --min-capacity 2 --max-capacity 6
```

### 停止中に管理から外した場合

停止中のサービスからセレクタのタグを外した場合、またはグループを削除した場合、そのサービスは desiredCount 0 かつ Auto Scaling 0-0 のまま残る。cheapskate は管理外のリソースに触れないため、この状態を復旧するコマンドは存在しない。上記のコマンドと `aws ecs update-service --desired-count` により手動で復旧する。

> [!CAUTION]
> ECS サービスを管理から外す場合は、起動させたあとに行うこと。

### 起動時のサイズ

起動時のサイズはリソース自身のタグから復元される。タグが無い場合は desiredCount 1 で起動するため、初めて停止させる前にタグを付与すること。詳細は、[resource_tag.md](resource_tag.md) のパラメータのタグを参照。

## 遷移中のまま停止したリソース

`starting`、`stopping`、`modifying`、`backing-up` のような遷移中の状態は、reconciler が毎サイクル skip する。エラーでも通知でもないため、`status#` の `transitioning_since` のみが手がかりとなる。

`transitioning_since` は、遷移を最初に観測したサイクルで 1 回だけ記録され、遷移でない状態を観測した時点で消える。参照できる箇所は次のとおりである。

- `cheapskate-cli show --group <名前>` の `status.transitioning_since`
- Web コンソールのグループページ
- `cheapskate-cli doctor` の `stuck-transitioning`

この値が長時間残っている場合、待機では解決しない事象が発生している可能性が高い。RDS ではメンテナンスウィンドウ、バックアップ、スナップショット作成中の停止要求を、ECS では長い deregistration delay や停止できないタスクを疑う。cheapskate 側に取りうる手段はないため、AWS のマネジメントコンソールまたは API で直接調査すること。遷移が解消すれば、次のサイクルが自動的に収束させる。

## セレクタの重複

同じリソースが 2 つ以上のグループのセレクタに一致した場合、グループ名順で最初のグループのみが有効となり、残りは無視される。無視された側のグループは、自身の `status#group#<名前>` にエラーを記録する。

```console
$ cheapskate-cli doctor | jq -r '.findings[] | select(.kind == "selector-overlap") | .detail'
matched by 2 groups [a-first z-second]; only "a-first" takes effect, the rest are ignored
```

この事象は、いずれかのセレクタを修正するまで解消しない。片方のグループの設定が全く適用されていない状態であるため、放置しないこと。
