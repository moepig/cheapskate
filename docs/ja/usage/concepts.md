# 基本概念

cheapskate は、タグ付き AWS リソースを、所属グループで決まる状態へ定期的に収束させる。設定は schedule と任意の override だけで構成する。

## グループと所属

リソースは `cheapskate:group` タグの値と同名のグループに所属する。空のタグ値、不正なグループ名、および未設定のグループ名はリソース単位のエラーになる。グループ名は 1～64 文字であり、ASCII の英字、数字、ピリオド、アンダースコア、およびハイフンを使用できる。

reconciler は、全グループと全タグ付きリソースを読み込んでから、Describe、Start、または Stop を呼び出す。全体の読み込みに失敗した場合はサイクルを中断する。不正なグループまたはリソースは個別に処理し、他のリソースは継続する。

## 望ましい状態の決定

有効な override を次の優先順で処理する。

1. `disabled` の場合、グループを reconcile 対象外にする。
2. `running` の場合、起動状態を選択する。
3. `stopped` の場合、停止状態を選択する。
4. 有効な override がない場合、直近の start cron と stop cron のうち新しい側を選択する。同時刻の場合は停止状態を選択する。

`override_expires_at <= now` の override は失効済みである。DynamoDB に属性が残っていても、schedule が自動的に有効になる。

schedule は、`DEFAULT_TIMEZONE` で解釈する 5 フィールド cron 2 個の組である。両方が存在し、過去と未来の発火時刻を取得できなければならない。グループは schedule または override の少なくとも一方を持つ。期限付き override には schedule も必要である。

## 収束と同時実行

現在の状態が安定しており、望ましい状態と異なるリソースだけにアクションを実行する。遷移中のリソースは、後続サイクルまで処理を見送る。

invocation は同時に実行される場合がある。Start または Stop を複数回配送したり、異なる設定を読んだ invocation が一時的に反対のアクションを実行したりする可能性がある。古い invocation の終了後に成功したサイクルが、最新設定へ収束させる。

## 対応リソース

- DB cluster の member ではなく、`custom-*` engine を使用しない RDS DB instance
- engine が `aurora` から始まる RDS DB cluster
- `REPLICA` scheduling strategy の ECS service
- EC2 instance

ECS の復元設定は、[リソースタグ](resource_tag.md)を参照すること。
