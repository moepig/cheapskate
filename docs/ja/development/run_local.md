# ローカル実行

すべてのコンポーネントは手元で実行できます。ローカルエミュレータ(Floci、`make floci-up`、エンドポイント `http://localhost:4566`)に対しても、自分の認証情報で実際の AWS アカウントに対しても動きます。エミュレータを使う場合は以下を export します:

```console
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
```

まず state テーブルを作成します(スキーマは本番と同じ — `docs/ja/usage/setup.md` §2 参照)。Floci に対しても同じ `aws dynamodb create-table` コマンドがそのまま使えます。

## Web コンソール

ローカルモードでは Lambda を介さないただの HTTP サーバーです:

```console
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/webconsole          # http://127.0.0.1:8080/
go run ./cmd/webconsole -addr 127.0.0.1:9090                       # ポート変更
```

## cheapskate-cli

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli list
go run ./cmd/cheapskate-cli pin rds-instance#dev-db stopped
```

## Reconciler

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
