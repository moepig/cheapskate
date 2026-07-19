# cheapskate の運用

望ましい状態は DynamoDB state テーブルの `config#` アイテムとして保持されます。付属の `csctl` CLI、Web コンソール、IaC、生の `put-item` のどれで管理しても構いません。単なるデータです。RDS/ECS の API を呼び出すのは reconciler Lambda だけです。

## csctl CLI

```console
go build -o csctl ./cmd/csctl        # またはリリースバイナリを取得
export CHEAPSKATE_TABLE=<state-テーブル名>

# Aurora クラスターを無期限に停止したままにする(7日後の自動起動を回避):
csctl pin rds-cluster#dev-aurora stopped

# ECS サービスを平日 09:00-20:00 JST で稼働させる:
csctl schedule ecs#dev-cluster/api -start "0 9 * * MON-FRI" -stop "0 20 * * MON-FRI" \
  -timezone Asia/Tokyo -restore-count 2

# ピン設定にかかわらず一時的に起動する(TTL で自動的に失効):
csctl override rds-cluster#dev-aurora running -for 2h

csctl list                           # 全リソースの override / last-action / 観測状態を表示
csctl show rds-cluster#dev-aurora    # config + override + status を JSON で表示
csctl disable ecs#dev-cluster/api    # config は残したまま管理対象から外す
csctl remove ecs#dev-cluster/api     # config, override, status を削除
```

`csctl` を使うオペレーターに必要なのは state テーブルへの `dynamodb:Scan`、`GetItem`、`PutItem`、`DeleteItem` だけです。RDS/ECS の権限は不要です。

## IaC やスクリプトから

Terraform を使う場合は `aws_dynamodb_table_item` で config アイテムを管理できます。reconciler は `config#` アイテムを書き込まないため、ドリフト検出はクリーンなままです。一時的な操作には引き続き `csctl override` を使ってください — override は TTL で失効するもので、IaC 管理には向きません。

同等の生の登録方法:

```console
aws dynamodb put-item --table-name <state-テーブル名> --item '{
  "pk":      {"S": "config#rds-cluster#dev-aurora"},
  "type":    {"S": "rds-cluster"},
  "mode":    {"S": "pinned"},
  "desired": {"S": "stopped"}
}'
```

## config アイテムのリファレンス

| 属性 | 対象 | 意味 |
|---|---|---|
| `pk` | 全て | `config#<type-prefix>#<identifier>`。ECS は `config#ecs#<cluster>/<service>` |
| `type` | 全て | `rds-instance` \| `rds-cluster` \| `ecs-service` |
| `mode` | 全て | `pinned`(固定の望ましい状態) \| `schedule`(cron) \| `disabled` |
| `desired` | pinned | `running` \| `stopped` |
| `start_cron` / `stop_cron` | schedule | 5フィールドの cron。最後に発火した方が優先される |
| `timezone` | schedule | IANA タイムゾーン名。デフォルトは reconciler の環境変数 `DEFAULT_TIMEZONE` |
| `restore_count` | ecs-service | 起動時に使う desiredCount。未設定時は停止時点の値を使用 |

## Web コンソール

デプロイしている場合([setup.md §9](setup.md#9-web-コンソールオプション))、同じ操作(一覧、pin、schedule、override、disable、remove)を JavaScript なしのサーバーレンダリングでブラウザから行えます。アクセス制御は IP 許可リストだけであることに注意してください。自分の AWS 認証情報でローカル実行もできます: `CHEAPSKATE_TABLE=<テーブル> go run ./cmd/webconsole` で `127.0.0.1:8080` に立ち上がります。

## 監視と挙動のメモ

- 通知(トピック設定時)はアクションが実行されたか失敗した場合にのみ送信され、収束済みサイクルでは何も送信されません。
- 恒常的な失敗を検知するには Lambda の `Errors` メトリクスにアラームを設定してください。リソースごとの直近のエラーは `status#` アイテムの `last_error` に記録され、`csctl show` で確認できます。
- 遷移中の状態(`starting`、`stopping` など)のリソースはスキップされ、次のサイクルで再度処理されます。
- Application Auto Scaling が設定された ECS サービスは、停止時に min/max が 0/0 に設定され、起動時に復元されます(そうしないとスケーリングポリシーが desiredCount の変更を元に戻してしまうため)。
