# トラブルシューティング

## サイクル集計の確認

reconciler は各サイクルで構造化ログを出力し、最後に `summary` レコードを出力する。Describe まで到達したリソース数、成功したアクション数、およびグループまたはリソースのエラー数を確認する。カスタムメトリクスを有効にしている場合、`ReconcileErrors` と `ReconcileAborted` を alarm の入力にできる。対象グループ、ARN、および原因はログで確認する。

設定 Query または全体のリソース検出に失敗した場合、リソースの Describe と変更を始める前にサイクルを中断する。DynamoDB または `tag:GetResources` の権限と到達性を修正してから再実行する。

## 主な設定エラー

主な症状と確認項目を次の表に示す。

| 症状 | 確認項目 |
| --- | --- |
| 不正な行としてグループが出力される | 未知属性、属性型、cron のフィールド数と到達可能性、cron 属性の組、override 値、および失効時刻の整数と範囲 |
| 期限付き override を保存できない | グループが 2 個の cron 属性を持ち、失効時刻が未来であること |
| override を削除できない | 先に schedule を追加する。override だけのグループは削除する |
| 書き込みで HTTP 409 または CLI 終了コード 2 になる | 別の要求が必要なアイテム状態を変更した。再度読み取り、意図した操作を明示的に再実行する |

検証を通らない経路で不正アイテムを直接修正してはならない。CLI と Web コンソールから修復できない場合は、アイテムをバックアップし、意図的に削除してから対応インターフェースで再作成する。

## リソース単位のエラー

次のエラーは個別に処理され、他のリソースは継続する。

- `cheapskate:group` の値が空、不正、または未設定のグループ名である。
- RDS engine が非対応、または RDS instance が cluster に所属している。
- ECS service が `REPLICA` ではない、または復元タグが不正である。
- adapter の権限が不足している、または service API が throttling している。

遷移中のリソースは意図的に処理を見送る。後続の 5 分サイクルで再度観測する。Start と Stop の配送は at-least-once であるため、adapter と運用手順は絶対状態の要求を繰り返しても処理できる必要がある。

## アクションが発生しない場合

グループに有効な `running` または `stopped` override、あるいは完全な schedule があることを確認する。有効な `disabled` override は、そのグループの Describe と変更を抑止する。schedule の場合は、`DEFAULT_TIMEZONE` と直近の start および stop の発火時刻を確認する。

リソースの固定所属タグと `tag:GetResources` の出力を確認する。検出時はタグキーだけを条件とするため、返されたタグ値が設定済みのグループ名と完全に一致する必要がある。

## ECS を復元できない場合

Start の前に service のタグを確認する。指定した値と既定値の組は `0 <= min <= desired <= max` を満たし、desired は正である必要がある。scalable target がない場合も同じ検証を行う。変更 API の前にタグを検証するため、タグエラーが発生した場合は service を変更しない。

## 通知とメトリクス

通知は Start または Stop に成功した後だけ送信する。Publish の失敗は同じ invocation 内で 1 回再試行する。2 回目も失敗した場合はログへ記録し、リソースへのアクションを取り消さない。復旧通知とエラー通知は送信しない。

カスタムメトリクスには `METRICS_ENABLED=true` が必要である。cheapskate は Embedded Metric Format のレコードを出力するが、alarm と dashboard は作成しない。
