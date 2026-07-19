# ビルド

前提: Go 1.26+。コンテナイメージには Docker(BuildKit)。

## バイナリ

```console
make build       # go build ./... — 全体をコンパイル
make cli         # bin/cheapskate-cli — オペレーター CLI
make webconsole  # bin/webconsole — Web コンソール(ローカルモード)
```

## コンテナイメージ

イメージは `public.ecr.aws/lambda/provided:al2023` 上に静的バイナリを 2 つ同梱します: `/var/runtime/bootstrap`(reconciler、デフォルトのエントリポイント)と `/var/runtime/webconsole`(オプションの Web コンソール関数で `ImageConfig` のエントリポイント上書きにより選択)。

```console
make image                                   # linux/arm64 の cheapskate:dev
make image PLATFORM=linux/amd64 TAG=v0.1.0   # x86 版
```

Dockerfile はホストプラットフォームから `GOARCH` でクロスコンパイルするため、x86 ホストでの arm64 イメージのビルド(またはその逆)にエミュレーションは不要です。

## イメージのスモークテスト

```console
make floci-up   # ローカル AWS エミュレータを起動(初回のみ)
make smoke      # イメージをビルドし、両エントリポイントを Lambda RIE 上で起動して呼び出す
```

`make smoke`(`scripts/smoke.sh`)はイメージをビルドした後、`public.ecr.aws/lambda/provided:al2023` ベースイメージに同梱されている `aws-lambda-rie` の上で 2 回起動します: 1 回目はデフォルトのエントリポイント(`/var/runtime/bootstrap`、reconciler)、2 回目は本番の `ImageConfig.EntryPoint` 上書きを模して `/var/runtime/webconsole` に上書きしたエントリポイントです。それぞれを `curl` で呼び出しレスポンスを検証します(reconciler は Summary JSON、webconsole は HTTP 200)。これにより、デプロイ前にエントリポイントの破損やビルドタグ漏れによるハンドラ欠落に気づけます。AWS CLI と起動中のエミュレータ(`make floci-up`)が必要です。

## 自分の ECR への push

```console
aws ecr create-repository --repository-name cheapskate   # 初回のみ
make push ECR_REPO=<account>.dkr.ecr.<region>.amazonaws.com/cheapskate TAG=v0.1.0
```

`make push` は `docker build`、`docker login`(`aws ecr get-login-password` 経由)、`docker push` をまとめたものです。イメージのプラットフォームは Lambda 関数のアーキテクチャと一致させてください(`arm64` ↔ `linux/arm64`)。
