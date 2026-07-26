# ローカル実行

構成は [../architecture/emulation_local.md](../architecture/emulation_local.md)。

## make dev

```console
make dev       # エミュレータ + state テーブル + サンプルグループ + Web コンソール
make dev-down  # エミュレータの停止
```

`scripts/dev.sh` が次を順に実行する。

1. `docker compose up -d` でエミュレータを起動し、ヘルスチェックを待つ
2. `go run ./cmd/dev-bootstrap` で state テーブル `cheapskate-dev` を作成する
3. `cheapskate-cli` でサンプルグループ `dev` を投入する
4. ダミー ECS リソースを作成する
5. Web コンソールを `http://127.0.0.1:8080/` でフォアグラウンド起動する

全工程が `go run` 経由であるため、コード変更は次のリクエストまたは次回実行に反映される。Web コンソールの停止は Ctrl-C、エミュレータの停止は `make dev-down` が行う。

サンプルグループのセレクタに一致するのはダミー ECS サービスのみである。ECS 以外の種別は空のリソース一覧または検出エラーとなる。

Web コンソールがフォアグラウンドを占有するため、別シェルから `cheapskate-cli` を使う場合は接続先と認証情報を渡す。

```console
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export CHEAPSKATE_TABLE=cheapskate-dev

go run ./cmd/cheapskate-cli list
```

これを付けずに実行すると実 AWS への接続を試み、認証情報の取得に失敗する。

## コンポーネント個別起動

各コンポーネントは、ローカルエミュレータにも実 AWS にも接続できる。エミュレータを使う場合は次を設定する。

```console
make floci-up
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
```

state テーブルは事前に作成する。`CHEAPSKATE_TABLE` を設定して `go run ./cmd/dev-bootstrap` を実行する。

### Web コンソール

```console
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/webconsole          # http://127.0.0.1:8080/
go run ./cmd/webconsole -addr 127.0.0.1:9090                       # ポート変更
```

### cheapskate-cli

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli list
go run ./cmd/cheapskate-cli set-selector --group dev --tag-key cheapskate:group --tag-value dev --types rds-instance
go run ./cmd/cheapskate-cli pin --group dev stopped
```

### Reconciler

reconciler は Lambda のエントリポイントであるため、Runtime Interface Emulator を使いコンテナ内で実行する。

```console
make image-reconciler
docker run --rm -p 9000:8080 \
  --add-host host.docker.internal:host-gateway \
  -e STATE_TABLE_NAME=cheapskate-state \
  -e AWS_ENDPOINT_URL=http://host.docker.internal:4566 \
  -e AWS_REGION=ap-northeast-1 -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  cheapskate-reconciler:dev
```

```console
# 別のシェルで — `{}` はフル reconcile をトリガーする
curl -d '{}' http://localhost:9000/2015-03-31/functions/function/invocations
```

`host.docker.internal` は、コンテナからホスト上のエミュレータへの到達に使う。実 AWS に対して動かす場合は `AWS_ENDPOINT_URL` を外し、実際の認証情報を渡す。

同じ起動と呼び出しを自動で行い、`testdata` の実イベントを投げて検証するのが `make image-test` である。reconcile ループの検証だけであれば、イメージのビルドを要しない統合テスト(`make integration`)も使える([test.md](test.md))。
