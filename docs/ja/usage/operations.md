# cheapskate の運用

望ましい状態は DynamoDB state テーブルの `tag#` / `member#` アイテムとして保持されます。**タグ**がスケジュール・pin・override の設定を 1 か所で持ち、各**リソース**はタグにメンバーとして参加します。1 つのリソースが属せるタグは常に 1 つです。付属の `cheapskate-cli` CLI、Web コンソール、IaC、生の `put-item` のどれで管理しても構いません。単なるデータです。RDS/ECS の API を呼び出すのは reconciler Lambda だけです。

## cheapskate-cli CLI

```console
go build -o cheapskate-cli ./cmd/cheapskate-cli        # またはリリースバイナリを取得
export CHEAPSKATE_TABLE=<state-テーブル名>

# タグにリソースを追加する(初回追加時にタグを作成、mode=disabled):
cheapskate-cli add --tag dev --type rds-cluster --name dev-aurora
cheapskate-cli add --tag dev --type ecs --cluster dev-cluster --service api -restore-count 2

# タグ内の全メンバーを無期限に停止したままにする(7日後の自動起動を回避):
cheapskate-cli pin --tag dev stopped

# タグを平日 09:00-20:00 JST で稼働させる — 全メンバーが同じスケジュールに従う:
cheapskate-cli schedule --tag dev -start "0 9 * * MON-FRI" -stop "0 20 * * MON-FRI" -timezone Asia/Tokyo

# ピン設定にかかわらずタグ内の全メンバーを一時的に起動する(TTL で自動的に失効):
cheapskate-cli override --tag dev running -for 2h

cheapskate-cli list                 # 全タグとそのメンバー・状態を解決して表示
cheapskate-cli show --tag dev       # タグの config + override + メンバー一覧を JSON で表示
cheapskate-cli disable --tag dev    # config は残したまま全メンバーを管理対象から外す

cheapskate-cli remove --tag dev --type ecs --cluster dev-cluster --service api  # メンバーを1件だけ除外
cheapskate-cli remove --tag dev                                                 # タグごと削除
```

リソース指定フラグ(`add`/`remove` 共通): `--type rds-instance|rds-cluster|ecs`、RDS 系は `--name IDENTIFIER`、`ecs` は `--cluster CLUSTER --service SERVICE`、`-restore-count N`(ecs のみ、メンバーごとに設定)。

既にスケジュール済み・pin 済みのタグに後からリソースを追加した場合、追加の手順なしに**次回の reconcile** から対象になります — 各メンバー個別の設定ではなく、タグの設定がそのまま適用されるためです。

`cheapskate-cli` を使うオペレーターに必要なのは state テーブルへの `dynamodb:Scan`、`GetItem`、`PutItem`、`DeleteItem` だけです。RDS/ECS の権限は不要です。

## IaC やスクリプトから

Terraform を使う場合は `aws_dynamodb_table_item` で `tag#`/`member#` アイテムを管理できます。reconciler はこれらを書き込まないため、ドリフト検出はクリーンなままです。一時的な操作には引き続き `cheapskate-cli override` を使ってください — override は TTL で失効するもので、IaC 管理には向きません。

同等の生の登録方法(タグとメンバー1件):

```console
aws dynamodb put-item --table-name <state-テーブル名> --item '{
  "pk":      {"S": "tag#dev"},
  "mode":    {"S": "pinned"},
  "desired": {"S": "stopped"}
}'
aws dynamodb put-item --table-name <state-テーブル名> --item '{
  "pk":   {"S": "member#rds-cluster#dev-aurora"},
  "tag":  {"S": "dev"},
  "type": {"S": "rds-cluster"}
}'
```

## アイテムのリファレンス

| アイテム | pk | 属性 |
|---|---|---|
| タグ | `tag#<name>` | `mode`(`pinned`\|`schedule`\|`disabled`)、`desired`(pinned 時: `running`\|`stopped`)、`start_cron`/`stop_cron`(schedule 時: 5フィールド cron。最後に発火した方が優先)、`timezone`(IANA 名。デフォルトは reconciler の `DEFAULT_TIMEZONE`) |
| メンバー | `member#<type-prefix>#<identifier>`(ECS は `member#ecs#<cluster>/<service>`) | `tag`(所属タグ名)、`type`(`rds-instance`\|`rds-cluster`\|`ecs-service`)、`restore_count`(ecs-service のみ: 起動時に使う desiredCount。未設定時は停止時点の値) |
| Override | `override#<tag-name>` | `desired`、`expires_at`(TTL) — タグの全メンバーに適用 |
| Status | `status#<type-prefix>#<identifier>` | reconciler 管理のリソースごとの監査情報 + ECS 復元データ。タグモデルでも変更なし |

## Web コンソール

デプロイしている場合([setup.md §9](setup.md#9-web-コンソールオプション))、コンソールはタグ単位でリソースをグループ表示します: 一覧ページは各タグとそのメンバーを表示し、タグページでは pin/schedule/override/disable/remove(全メンバーに適用)とメンバーごとの追加/削除が行えます — すべて JavaScript なしのサーバーレンダリングです。アクセス制御は IP 許可リストだけであることに注意してください。自分の AWS 認証情報でローカル実行もできます: `CHEAPSKATE_TABLE=<テーブル> go run ./cmd/webconsole` で `127.0.0.1:8080` に立ち上がります。サンプルタグ入りで一式をローカル起動するには `make dev` も使えます([run_local.md](../development/run_local.md) 参照)。

## 監視と挙動のメモ

- 通知(トピック設定時)はアクションが実行されたか失敗した場合にのみ送信され、タグ名を含むようになりました: `[cheapskate] stop: dev/rds-instance#dev-db`。収束済みサイクルでは何も送信されません。
- 恒常的な失敗を検知するには Lambda の `Errors` メトリクスにアラームを設定してください。リソースごとの直近のエラーは `status#` アイテムの `last_error` に記録され、`cheapskate-cli show --tag` で確認できます。
- 遷移中の状態(`starting`、`stopping` など)のリソースはスキップされ、次のサイクルで再度処理されます。
- Application Auto Scaling が設定された ECS サービスは、停止時に min/max が 0/0 に設定され、起動時に復元されます(そうしないとスケーリングポリシーが desiredCount の変更を元に戻してしまうため)。
- タグレベルの問題(不正な cron など)は、メンバーごとに1回ずつ記録・通知されます — タグを修正すれば、次の収束サイクルで全メンバーのエラーがクリアされます。
