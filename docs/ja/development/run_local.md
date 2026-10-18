# ローカル実行

本ドキュメントは、ローカル AWS エミュレータを使用した開発環境と、コンポーネント別の起動方法を示す。

エミュレータは結合テストとイメージテストでも使用する。AWS と異なる control plane の動作は、adapter の単体テストで API request の選択と検証を確認する。

## 開発環境全体の起動

開発環境の起動には Docker が必要である。`make dev` は AWS エミュレータを起動し、DynamoDB テーブル、タグ付き ECS fixture、およびサンプル schedule を作成して、Web コンソールを実行する。

起動時には `make dev` を実行する。開発環境の使用後に `make dev-down` を実行してエミュレータを停止する。

```console
make dev
make dev-down
```

Web コンソールは `http://127.0.0.1:8080/` で待ち受ける。`make dev` の既定タイムゾーンは Asia/Tokyo、サンプル schedule は平日 09:00 起動・21:00 停止である。既存の環境変数で上書きできる。

別の shell から操作する場合は、同じエミュレータ設定を使用する。環境変数 AWS_ENDPOINT_URL を指定しない場合、AWS SDK client が実 AWS の認証情報とリソースを使用する可能性がある。

```console
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export CHEAPSKATE_TABLE=cheapskate-dev
export DEFAULT_TIMEZONE=Asia/Tokyo

go run ./cmd/cheapskate-cli list
go run ./cmd/cheapskate-cli show --group dev
```

## コンポーネント別の起動

個別に起動する場合も、上記の AWS 接続用環境変数とタイムゾーンを同じ shell に設定する。

### エミュレータとテーブルを準備する

各コンポーネントを起動する前に、エミュレータとテーブルを準備する。

```console
make floci-up
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/dev-bootstrap
```

### Web コンソールを起動する

グループ設定を作成し、Web コンソールを起動する。

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli schedule --group dev -start '0 8 * * 1-5' -stop '0 20 * * 1-5'
STATE_TABLE_NAME=cheapskate-state go run ./cmd/webconsole
```

### reconciler を呼び出す

reconciler はコンテナイメージ内の Lambda Runtime Interface Emulator を介して呼び出す。イメージをビルドし、`docker run` で起動した後、別の shell で末尾の `curl` を実行する。

```console
make image-reconciler
docker run --rm -p 9000:8080 \
  --add-host host.docker.internal:host-gateway \
  -e STATE_TABLE_NAME=cheapskate-state \
  -e AWS_ENDPOINT_URL=http://host.docker.internal:4566 \
  -e DEFAULT_TIMEZONE=Asia/Tokyo \
  -e AWS_REGION=ap-northeast-1 \
  -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  cheapskate-reconciler:dev

curl -d '{}' http://localhost:9000/2015-03-31/functions/function/invocations
```

payload の内容にかかわらず、全体の reconcile を実行する。
