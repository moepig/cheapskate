# ローカル実行

## 開発環境全体の起動

Docker が必要である。次の target は emulator と table を起動し、タグ付き ECS fixture とサンプル schedule を作成して、Web コンソールを実行する。

```console
make dev
make dev-down
```

Web コンソールは `http://127.0.0.1:8080/` で待ち受ける。別の shell から操作する場合は、同じ emulator 設定を使用する。

```console
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export CHEAPSKATE_TABLE=cheapskate-dev
export DEFAULT_TIMEZONE=UTC

go run ./cmd/cheapskate-cli list
go run ./cmd/cheapskate-cli show --group dev
```

endpoint 変数を指定しない場合、AWS SDK client が実 AWS の認証情報とリソースを使用する可能性がある。

## コンポーネント別の起動

個別に起動する場合は、先に emulator と table を準備する。

```console
make floci-up
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/dev-bootstrap
```

設定を作成し、Web コンソールを起動する例を次に示す。

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli schedule --group dev -start '0 8 * * 1-5' -stop '0 20 * * 1-5'
STATE_TABLE_NAME=cheapskate-state go run ./cmd/webconsole
```

reconciler entrypoint は、container image 内の Lambda Runtime Interface Emulator を介して動作する。

```console
make image-reconciler
docker run --rm -p 9000:8080 \
  --add-host host.docker.internal:host-gateway \
  -e STATE_TABLE_NAME=cheapskate-state \
  -e AWS_ENDPOINT_URL=http://host.docker.internal:4566 \
  -e AWS_REGION=ap-northeast-1 \
  -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  cheapskate-reconciler:dev

curl -d '{}' http://localhost:9000/2015-03-31/functions/function/invocations
```

payload の内容にかかわらず、同じ full reconcile を実行する。
