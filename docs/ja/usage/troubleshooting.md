# 障害対応と状態の復旧

reconcile が途中で失敗した、リソースが中途半端な状態で止まった、state テーブルにゴミが残った、というときの手順。用語は [concepts.md](concepts.md)、日常の設定操作は [operations.md](operations.md)、リソース側のタグは [resource_tag.md](resource_tag.md)。

cheapskate は収束ループであり、失敗しても次のサイクルが同じことをやり直す。ほとんどの一過性の失敗は放置すれば直る。このページが扱うのは、放置しても直らないものと、その見分け方である。

## 気づく

| 経路 | 何が分かるか |
| --- | --- |
| SNS 通知 | アクションの実行・失敗・復旧。同じエラーが続く間は再通知されない |
| Lambda `Errors` メトリクス | サイクル全体の異常(payload 不正、`Scan` 失敗、タイムアウト、panic)と、リソース単位の失敗が 1 件以上あったサイクル |
| `ReconcileErrors` / `ReconcileActions` / `ReconciledResources` / `ReconcileAborted` メトリクス | 件数の推移 |
| `status#` の `last_error` | リソースごとの直近エラー。`cheapskate-cli list` / `show`、Web コンソールで見る |
| `cheapskate-cli doctor` | state テーブルの不整合と取り残されたレコード |
| CloudWatch Logs | 上記のいずれにも現れない失敗 |

`Errors` へのアラーム 1 本で、サイクル全体の異常とリソース単位の失敗の両方を拾える。ただし `Errors` は失敗の件数を区別しないため、件数の推移には `ReconcileErrors` を使う。

`Errors` が立つと、EventBridge の非同期リトライで同じフル reconcile が最大 2 回追加で走る。収束済みのリソースにはアクションが起きず、継続中のエラーは通知の重複排除に当たるため、リトライで通知が増えることはない。

SNS トピックを設定していない場合、通知は no-op となる。その状態で `Errors` アラームも張っていないと、全リソースが失敗し続けても何も鳴らない。トピックとアラームのどちらか一方は必ず用意すること。

### メトリクスにも通知にも現れない失敗

記録系の失敗(SNS Publish や `status#` 書き込みの失敗)と、終わらない遷移は、どちらも操作自体は成功しているため、メトリクスにも通知にも現れない。前者はログ、後者は `doctor` で拾う。

### アラームの設定

最低限の 1 本。サイクル全体の異常もリソース単位の失敗もこれで拾える。

```console
aws cloudwatch put-metric-alarm --alarm-name cheapskate-reconciler-errors \
  --namespace AWS/Lambda --metric-name Errors --statistic Sum \
  --dimensions Name=FunctionName,Value=cheapskate-reconciler \
  --period 300 --evaluation-periods 2 --threshold 0 --comparison-operator GreaterThanThreshold \
  --treat-missing-data notBreaching --alarm-actions <SNS トピック ARN>
```

`evaluation-periods 2` としているのは、一過性の失敗が次サイクルで自動的に収束するためである。1 サイクルで鳴らすと、放置すれば直るものまで通知することになる。

件数の増加を別に見る場合:

```console
aws cloudwatch put-metric-alarm --alarm-name cheapskate-reconcile-errors \
  --namespace cheapskate --metric-name ReconcileErrors --statistic Maximum \
  --period 300 --evaluation-periods 2 --threshold 0 --comparison-operator GreaterThanThreshold \
  --treat-missing-data notBreaching --alarm-actions <SNS トピック ARN>
```

`ReconciledResources` / `ReconcileActions` / `ReconcileErrors` は、サイクルが立ち上がらなかったときにデータポイント自体が存在しない。この 3 つにアラームを張る場合は `treatMissingData=notBreaching` とする。

呼び出し自体が止まったことを検知する場合は、`ReconcileAborted` に `treatMissingData=breaching` のアラームを張る。呼び出しが止まればデータポイントが途切れるため、定期実行トリガーの停止やイベントルールの事故に気づける。

## 中途半端に終わったときに何が残るか

| 事象 | 自動で直るか | 人がやること |
| --- | --- | --- |
| Lambda がタイムアウトしてグループの途中で切れた | 直る。次サイクルが最初からやり直す | 毎回切れるならメモリ/タイムアウトを上げる |
| `Scan` に失敗してサイクルが立ち上がらなかった | 直る。リトライと次サイクルがある | なし |
| Stop/Start が失敗した | 直る。次サイクルが再試行する | 権限不足など恒久的な原因なら `last_error` を見て直す |
| アクションは成功したが `status#` の書き込みに失敗した | 直る。ただし 1〜2 サイクルの間、成功したアクションに対して誤ったエラーが記録され、そのあと復旧が通知される | なし(通知のノイズとして許容する) |
| ECS の停止が desiredCount 更新の手前で失敗した | 直る。スケーラブルターゲットは元の min/max へ自動で巻き戻る | なし。巻き戻しにも失敗した場合のみ [ECS サービス特有の注意](#ecs-サービス特有の注意) |
| リソースが遷移中のまま止まった | 直らない。毎サイクル黙って skip され続ける | [遷移中のまま止まったリソース](#遷移中のまま止まったリソース) |
| グループを消したが `override#` / `status#` が残った | 直らない | `doctor --prune` |
| タグを外したリソースの `status#` が残った | 直らない(無害ではある) | `doctor --prune` |
| セレクタが重複して片方のグループが無視されている | 直らない | [セレクタの重複](#セレクタの重複) |
| ECS サービスを停止中に管理から外した | 直らない。desiredCount 0 / Auto Scaling 0-0 のまま残る | [ECS サービス特有の注意](#ecs-サービス特有の注意) |

## doctor による診断

state テーブルの不整合と、中途半端に終わった処理が残したゴミを 1 コマンドで洗い出す。既定では読み取りだけを行う。

```console
cheapskate-cli doctor                      # 診断のみ
cheapskate-cli doctor --prune              # 孤立レコードを削除する
cheapskate-cli doctor --stuck-after 2h     # 遷移中とみなす上限を変える(既定 30m)
```

Web コンソールでは、diagnostics ページから同じ診断が見られ、孤立レコードの削除も同じ条件で実行できる。

| `kind` | 意味 | `--prune` で消えるか |
| --- | --- | --- |
| `orphan-override` | `group#` がないのに `override#` が残っている | 消える |
| `orphan-group-status` | `group#` がないのに `status#group#` が残っている | 消える |
| `orphan-status` | どのグループのセレクタにも一致しないリソースの `status#` | 消える |
| `corrupt-record` | 読み取りまたは検証に失敗するレコード | 消えない |
| `config-error` | 登録済みだが reconciler が従えない設定(pinned なのに desired がない等) | 消えない |
| `discover-error` | セレクタは妥当だがリソースの検出が失敗した | 消えない |
| `selector-overlap` | 複数グループのセレクタが同じリソースを取り合っている | 消えない |
| `stuck-transitioning` | `--stuck-after` を超えて遷移中のまま | 消えない |

`--prune` が消すのは、そのグループやリソースが存在しないことがテーブルの読み取りと検出だけで確定するレコードに限る。設定そのもの(`group#`)や、人間の判断を要する項目には一切触れない。AWS リソースにも触れない。

安全装置として、検出が 1 つでも失敗したサイクルでは `orphan-status` の判定そのものを見送る。一時的に検出できなかっただけのリソースの監査記録を消してしまわないためである。この場合 `blocked` に理由が入る。

```console
$ cheapskate-cli doctor | jq '{blocked, counts}'
{
  "blocked": ["group \"dev\" could not be discovered (AccessDenied); its members are unknown"],
  "counts": {"discover-error": 1}
}
```

`blocked` が空でないときに `orphan-status` が 0 件なのは、孤立レコードがないという意味ではなく、調べられなかったという意味である。原因を直してから流し直すこと。

各項目の `pk` に生の DynamoDB キーが入っているため、`--prune` を使わず手で消すこともできる。

```console
aws dynamodb delete-item --table-name <state-テーブル名> \
  --key "$(cheapskate-cli doctor | jq -c '{pk: {S: .findings[0].pk}}')"
```

## 緊急時の手順

介入の反映はすべて次の reconcile サイクル(既定 5 分)を待つ。待てないときは reconciler を手で呼び出す。

```console
aws lambda invoke --function-name cheapskate-reconciler --payload '{}' /dev/stdout
```

`{}` はフル reconcile を意味する。応答の JSON がそのサイクルの `actions` と `errors` になる。

### とにかく全部起動したい

```console
cheapskate-cli override --group dev running -for 8h
```

順序の罠がある。`disable` したグループには override を登録できない。`disabled` は override より強い停止だからである。障害対応でまず `disable` を打ってしまうと、起動させるには先に `pin` か `schedule` へ戻す必要がある。

```console
cheapskate-cli pin --group dev running     # disable してしまった場合はこちら
```

逆に、cheapskate に一切触らせずに人手で作業したい場合は `disable` が正しい。cheapskate はそのグループを一切見なくなるが、すでに実行したアクションは巻き戻さない。停止済みのリソースは停止したまま残る。

### グループを消す

```console
cheapskate-cli remove --group dev
```

`override#` → `status#group#` → `group#` の順に消すため、途中で失敗してもグループ本体が残り、再試行でたどり着ける。リソース単位の `status#` は残るため、気になる場合は `doctor --prune` を使う。

AWS リソースには一切触れない。削除後もリソースは cheapskate が最後に置いた状態のまま残る。停止させたまま管理を外すつもりがないなら、`remove` の前に `override running` で起動させてサイクルを 1 回待つこと。

## ECS サービス特有の注意

停止は「Application Auto Scaling の min/max を 0/0 にする」→「desiredCount を 0 にする」の 2 段階であり、原子的ではない。

後段が失敗した場合、スケーラブルターゲットは元の min/max へ自動で巻き戻る。サービスは起動したままで、スケールアウトもできる状態が保たれる。

巻き戻しにも失敗した場合だけ、`last_error` に `left clamped at 0/0` と戻すべき値が出る。この状態のサービスは起動していてもスケールアウトできないため、手で戻すこと。

```console
aws application-autoscaling register-scalable-target --service-namespace ecs \
  --resource-id service/dev-cluster/api --scalable-dimension ecs:service:DesiredCount \
  --min-capacity 2 --max-capacity 6
```

### 停止中に管理から外した場合

停止中のサービスからセレクタのタグを外す、またはグループを消すと、desiredCount 0・Auto Scaling 0-0 のまま取り残される。管理外のリソースには触れないため、cheapskate 側にこれを戻すコマンドはない。上のコマンドと `aws ecs update-service --desired-count` で手動復旧する。管理を外すなら、起動させてからにすること。

### 起動時のサイズ

起動時のサイズはリソース自身のタグから復元される。タグが無いと desiredCount 1 で起動するため、初めて停止させる前にタグを付けておくこと([resource_tag.md](resource_tag.md))。

## 遷移中のまま止まったリソース

`starting` / `stopping` / `modifying` / `backing-up` のような遷移中の状態は、reconciler が毎サイクル skip する。エラーでも通知でもないため、`status#` の `transitioning_since` だけが手がかりになる。

`transitioning_since` は、遷移を最初に観測したサイクルで 1 回だけ記録され、遷移でない状態を観測した時点で消える。確認できる場所は次のとおりである。

- `cheapskate-cli show --group <名前>` の `status.transitioning_since`
- Web コンソールのグループページ
- `cheapskate-cli doctor` の `stuck-transitioning`

長時間これが残っている場合、待っても解決しない事象が起きている可能性が高い。RDS ならメンテナンスウィンドウやバックアップ、スナップショット作成中の停止要求、ECS なら長い deregistration delay や停止できないタスクを疑う。cheapskate 側でできることはないため、AWS のコンソールまたは API で直接調べること。遷移が解消すれば、次のサイクルが自動的に収束させる。

## セレクタの重複

同じリソースが 2 つ以上のグループのセレクタに一致すると、グループ名順で最初のグループだけが効き、残りは黙って無視される。無視された側は自分の `status#group#<名前>` にエラーを記録する。

```console
$ cheapskate-cli doctor | jq -r '.findings[] | select(.kind == "selector-overlap") | .detail'
matched by 2 groups [a-first z-second]; only "a-first" takes effect, the rest are ignored
```

どちらかのセレクタを直すまで解消しない。片方のグループの設定が丸ごと効いていない状態であるため、放置しないこと。
