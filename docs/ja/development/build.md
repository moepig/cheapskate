# ビルド

## バイナリ

```console
make build       # go build ./... の実行
make cli         # bin/cheapskate-cli を作成
make webconsole  # bin/webconsole を作成
```

## コンテナイメージ

reconciler と Web コンソールは別々のイメージであり、バイナリを 1 つずつ含む。どちらも同じ `Dockerfile` から `--target` で選択してビルドする。

| ターゲット | イメージ | バイナリ |
| --- | --- | --- |
| `reconciler` | `cheapskate-reconciler` | `./cmd/reconciler` |
| `webconsole` | `cheapskate-webconsole` | `./cmd/webconsole` |

```console
make image                                   # 両方、linux/arm64、タグ dev
make image-reconciler                        # cheapskate-reconciler:dev のみ
make image-webconsole                        # cheapskate-webconsole:dev のみ
make image PLATFORM=linux/amd64 TAG=v0.1.0   # x86 版
```

ベースは `public.ecr.aws/lambda/provided:al2023` で、バイナリはどちらも `/var/runtime/bootstrap` として入るため、`ImageConfig.EntryPoint` による上書きを要しない。Web コンソールをデプロイしない場合は、reconciler だけをビルド・push すればよい。

Dockerfile はホストプラットフォームから `GOARCH` でクロスコンパイルするため、異アーキテクチャのビルドにエミュレーションを要しない。Go のビルドステージは 2 つのイメージで共有するので、両方ビルドしても依存のダウンロードは 1 回で済む。

### Lambda Web Adapter

Web コンソールのイメージには、Lambda Web Adapter の実行ファイルが `/opt/extensions/lambda-adapter` として入る([../architecture/on_lambda.md](../architecture/on_lambda.md))。バージョンは `Dockerfile` で固定してある。go.mod の外にある唯一の実行時依存であり、更新は Go モジュールとは別に Dependabot の docker 更新で行う([release.md](release.md))。

## イメージのテスト

```console
make image-test   # 両イメージをビルドし、それぞれ Lambda RIE 上で起動して呼び出す
```

イメージの破損やハンドラの欠落をデプロイ前に検出する。必要なのは docker だけであり、エミュレータと使い捨ての state テーブルはテストと一緒に立ち上がる。検証内容は [test.md](test.md)。

## ECR への push

リポジトリはイメージごとに 1 つ作成する。

```console
aws ecr create-repository --repository-name cheapskate-reconciler   # 初回のみ
aws ecr create-repository --repository-name cheapskate-webconsole   # 初回のみ(Web コンソールをデプロイする場合)
make push \
  ECR_REPO_RECONCILER=<account>.dkr.ecr.<region>.amazonaws.com/cheapskate-reconciler \
  ECR_REPO_WEBCONSOLE=<account>.dkr.ecr.<region>.amazonaws.com/cheapskate-webconsole \
  TAG=v0.1.0
```

`make push` は両イメージについて `docker build`、`docker login`、`docker push` を実行する。片方だけの場合は `make push-reconciler` / `make push-webconsole` を使い、対応する `ECR_REPO_*` だけを指定する。

イメージのプラットフォームは、Lambda 関数のアーキテクチャと一致させる(`arm64` ↔ `linux/arm64`)。
