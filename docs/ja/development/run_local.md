# ローカル実行

本ドキュメントは、開発環境で cheapskate を動かす手順を記述する。経路は 2 つあり、`make dev` による一括起動と、コンポーネントごとの個別起動である。いずれもローカル AWS エミュレータ Floci に接続する。

## make dev

エミュレータからサンプルデータの投入までを一度に行う。

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

サンプルグループのセレクタに一致するのはダミー ECS サービスのみである。ECS 以外の種別は、空のリソース一覧または検出エラーとなる。

Web コンソールがフォアグラウンドを占有するため、別シェルから `cheapskate-cli` を使う場合は接続先と認証情報を渡す。設定と実行の例を、以下に示す。

```console
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
export CHEAPSKATE_TABLE=cheapskate-dev

go run ./cmd/cheapskate-cli list
```

> [!WARNING]
> これらを設定せずに実行した場合、エミュレータではなく実 AWS への接続を試みる。認証情報が有効であれば、実アカウントのリソースを操作する。

## コンポーネント個別起動

各コンポーネントは、ローカルエミュレータにも実 AWS にも接続できる。エミュレータを使う場合の設定は次のとおりである。

```console
make floci-up
export AWS_ENDPOINT_URL=http://localhost:4566
export AWS_REGION=ap-northeast-1 AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test
```

ECS 以外の種別のダミーリソースが存在しないのは、エミュレータの再現範囲による制約である。接続先の切り替えが `AWS_ENDPOINT_URL` だけで済む理由、再現できない AWS API の範囲、およびその代替手段は、[../architecture/emulation_local.md](../architecture/emulation_local.md) を参照。

state テーブルは事前に作成する。`CHEAPSKATE_TABLE` を設定して `go run ./cmd/dev-bootstrap` を実行する。

### Web コンソール

state テーブル名を環境変数で与えて起動する。

```console
CHEAPSKATE_TABLE=cheapskate-state go run ./cmd/webconsole          # http://127.0.0.1:8080/
go run ./cmd/webconsole -addr 127.0.0.1:9090                       # ポート変更
```

### cheapskate-cli

同じく state テーブル名を与えて、サブコマンドを実行する。

```console
export CHEAPSKATE_TABLE=cheapskate-state
go run ./cmd/cheapskate-cli list
go run ./cmd/cheapskate-cli set-selector --group dev --tag-key cheapskate:group --tag-value dev --types rds-instance
go run ./cmd/cheapskate-cli pin --group dev stopped
```

### Reconciler

reconciler は Lambda のエントリポイントであるため、Runtime Interface Emulator を使いコンテナ内で実行する。イメージのビルドと起動を、以下に示す。

```console
make image-reconciler
docker run --rm -p 9000:8080 \
  --add-host host.docker.internal:host-gateway \
  -e STATE_TABLE_NAME=cheapskate-state \
  -e AWS_ENDPOINT_URL=http://host.docker.internal:4566 \
  -e AWS_REGION=ap-northeast-1 -e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test \
  cheapskate-reconciler:dev
```

起動したコンテナへは、別のシェルから HTTP で呼び出しを投入する。

```console
# `{}` はフル reconcile をトリガーする
curl -d '{}' http://localhost:9000/2015-03-31/functions/function/invocations
```

`host.docker.internal` は、コンテナからホスト上のエミュレータへの到達に使う。実 AWS に対して動かす場合は `AWS_ENDPOINT_URL` を外し、実際の認証情報を渡す。

同じ起動と呼び出しを自動化し、`testdata` の実イベントを投入して検証するのが `make image-test` である。reconcile ループの検証のみが目的であれば、イメージのビルドを要しない統合テスト `make integration` でも足りる。両者の対象範囲は、[test.md](test.md) のテストレイヤを参照。
