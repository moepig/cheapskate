# ビルド

本ドキュメントは、バイナリとコンテナイメージのビルド手順、および ECR への push を記述する。ビルドの入口はいずれも `make` のターゲットである。

## バイナリ

`go build` を包む 3 つのターゲットがある。

```console
make build       # go build ./... の実行
make cli         # bin/cheapskate-cli を作成
make webconsole  # bin/webconsole を作成
```

## コンテナイメージ

reconciler と Web コンソールは別々のイメージであり、バイナリを 1 つずつ含む。どちらも同じ `Dockerfile` から `--target` で選択してビルドする。

イメージのビルドターゲットと、それぞれが含むバイナリの対応を、以下にまとめる。

| ターゲット | イメージ | バイナリ |
| --- | --- | --- |
| `reconciler` | `cheapskate-reconciler` | `./cmd/reconciler` |
| `webconsole` | `cheapskate-webconsole` | `./cmd/webconsole` |

ビルドの実行例を、以下に示す。

```console
make image                                   # 両方、linux/arm64、タグ dev
make image-reconciler                        # cheapskate-reconciler:dev のみ
make image-webconsole                        # cheapskate-webconsole:dev のみ
make image PLATFORM=linux/amd64 TAG=v0.1.0   # x86 版
```

ベースイメージは `public.ecr.aws/lambda/provided:al2023` である。バイナリはどちらも `/var/runtime/bootstrap` として配置されるため、`ImageConfig.EntryPoint` による上書きを要しない。Web コンソールをデプロイしない場合、ビルドと push は reconciler のみでよい。

Dockerfile はホストプラットフォームから `GOARCH` でクロスコンパイルするため、異アーキテクチャのビルドにエミュレーションを要しない。Go のビルドステージは 2 つのイメージで共有するため、両方をビルドしても依存のダウンロードは 1 回で済む。

### Lambda Web Adapter

Web コンソールのイメージには、Lambda Web Adapter の実行ファイルが `/opt/extensions/lambda-adapter` として入る。バージョンは `Dockerfile` で固定してある。これは go.mod の外にある唯一の実行時依存であり、更新は Go モジュールとは別に Dependabot の docker 更新で行う。

アダプタが呼び出しイベントを HTTP へ変換する仕組みと、本体側がアダプタに依存する箇所は、[../architecture/on_lambda.md](../architecture/on_lambda.md) を参照。Dependabot による更新がリリースに至る経路は、[release.md](release.md) の依存の更新を参照。

## イメージのテスト

ビルドしたイメージが Lambda ランタイムとして起動することを、デプロイ前に確認する。

```console
make image-test   # 両イメージをビルドし、それぞれ Lambda RIE 上で起動して呼び出す
```

イメージの破損やハンドラの欠落を検出する。必要な前提は docker のみであり、エミュレータと使い捨ての state テーブルはテストと一緒に立ち上がる。投入するペイロードと期待する応答は、[test.md](test.md) のイメージのテストを参照。

## ECR への push

リポジトリはイメージごとに 1 つ作成する。作成から push までの実行例を、以下に示す。

```console
aws ecr create-repository --repository-name cheapskate-reconciler   # 初回のみ
aws ecr create-repository --repository-name cheapskate-webconsole   # 初回のみ(Web コンソールをデプロイする場合)
make push \
  ECR_REPO_RECONCILER=<account>.dkr.ecr.<region>.amazonaws.com/cheapskate-reconciler \
  ECR_REPO_WEBCONSOLE=<account>.dkr.ecr.<region>.amazonaws.com/cheapskate-webconsole \
  TAG=v0.1.0
```

`make push` は両イメージについて `docker build`、`docker login`、`docker push` を実行する。片方のみを対象とする場合は `make push-reconciler` または `make push-webconsole` を使い、対応する `ECR_REPO_*` だけを指定する。

> [!IMPORTANT]
> イメージのプラットフォームは、Lambda 関数のアーキテクチャと一致させること (`arm64` ↔ `linux/arm64`)。不一致のイメージは関数の作成または更新の時点で拒否される。
