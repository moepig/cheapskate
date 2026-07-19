# ローカル実行

## クイックスタート: `make dev`

```console
make dev       # floci + state テーブル + サンプルタグ "dev"(rds-instance + ecs メンバー、スケジュール済み) + Web コンソール
```

Floci を起動し(`docker compose`)、ヘルスチェックを待ち、`cheapskate-dev` state テーブルを(`internal/statetable` 経由で、冪等に — 再実行しても安全)作成し、実際の `cheapskate-cli` でサンプルタグを投入し(投入コマンド自体が使用例を兼ねます)、Web コンソールを `http://127.0.0.1:8080/` でフォアグラウンド起動します。すべて `go run` 経由なので、コードの変更は次のリクエストや次の `make dev` に反映されます。

Ctrl-C でコンソールを停止、`make dev-down` で Floci を停止します。投入したサンプルリソースは Floci 上に実体を持たないため、これらに対する手動 reconcile(下記参照)は not-found として報告されます — これは想定内の挙動で、reconcile ループ全体をエンドツーエンドで検証するには統合テスト(`make integration`)の方が適しています。

## 手動でのコンポーネント個別起動

各コンポーネントは個別に手元で実行することもできます。ローカルエミュレータ(Floci、`make floci-up`、エンドポイント `http://localhost:4566`)に対しても、自分の認証情報で実際の AWS アカウントに対しても動きます。エミュレータを使う場合は以下を export します:

```console
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
```

まず state テーブルを作成します(スキーマは本番と同じ — `docs/ja/usage/setup.md` §2 参照)。`CHEAPSKATE_TABLE` を設定して `go run ./cmd/dev-bootstrap` を実行するか、setup.md と同じ `aws dynamodb create-table` コマンドを Floci に対して直接使います。

### Web コンソール

ローカルモードでは Lambda を介さないただの HTTP サーバーです:

```console
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/webconsole          # http://127.0.0.1:8080/
go run ./cmd/webconsole -addr 127.0.0.1:9090                       # ポート変更
```

### cheapskate-cli

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli list
go run ./cmd/cheapskate-cli add --tag dev --type rds-instance --name dev-db
go run ./cmd/cheapskate-cli pin --tag dev stopped
```

### Reconciler

reconciler は Lambda エントリポイントなので、`provided:al2023` ベースイメージに同梱されている Runtime Interface Emulator を使ってコンテナイメージ内で実行します:

```console
make image
docker run --rm -p 9000:8080 \
  --add-host host.docker.internal:host-gateway \
  -e STATE_TABLE_NAME=cheapskate-state \
  -e AWS_ENDPOINT_URL=http://host.docker.internal:4566 \
  -e AWS_REGION=ap-northeast-1 -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  cheapskate:dev

# 別のシェルで — `{}` はフル reconcile をトリガー:
curl -d '{}' http://localhost:9000/2015-03-31/functions/function/invocations
```

(`host.docker.internal` によりコンテナからホスト上の Floci に到達できます。実際の AWS に対して動かす場合は `AWS_ENDPOINT_URL` を外し、実際の認証情報を渡してください。)

なお、統合テスト(`make integration`)はイメージを起動せずに Floci に対して reconcile ループ全体を検証するため、通常はそちらの方が速いフィードバックループです。
