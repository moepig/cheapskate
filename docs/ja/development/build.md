# ビルド

本ドキュメントは、バイナリとコンテナイメージのビルド手順、および ECR への push を記述する。ビルドの入口はいずれも `make` のターゲットである。

## バイナリ

`go build` を実行する 3 つのターゲットがある。

```console
make build       # go build ./... の実行
make cli         # bin/cheapskate-cli を作成
make webconsole  # bin/webconsole を作成
```

## コンテナイメージ

reconciler と Web コンソールは別々のイメージであり、バイナリを 1 つずつ含む。どちらも同じ `Dockerfile` から `--target` で選択してビルドする。Web コンソールをデプロイしない場合、ビルドと push は reconciler のみでよい。

イメージのビルドターゲットと、それぞれが含むバイナリの対応を、以下にまとめる。

| Dockerfile のターゲット | イメージ | バイナリ |
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

バイナリはどちらも `/var/runtime/bootstrap` として配置する。このため、`ImageConfig.EntryPoint` による上書きは不要である。

ビルド入力はタグと digest の両方で固定する。対象は Go ビルダー、Lambda ベースイメージ、および Dockerfile frontend である。タグは更新対象のバージョンを示し、digest は参照する内容を固定する。Lambda ベースイメージは `public.ecr.aws/lambda/provided:al2023` である。

Dockerfile はホストプラットフォームから `GOARCH` でクロスコンパイルする。このため、異なるアーキテクチャ向けのビルドにエミュレーションは不要である。

Go のビルドステージは 2 つのイメージで共有する。このため、両方をビルドしても依存のダウンロードは 1 回で済む。

ビルドコンテキストは `.dockerignore` の allowlist により `Dockerfile`、モジュールファイル、`cmd/`、`internal/` に限定する。Dockerfile も `COPY . .` を使わず、必要なディレクトリだけを明示してコピーする。開発用ファイルや資格情報を誤ってコンテキストやイメージへ含めず、無関係な変更によるキャッシュ無効化も避けるためである。

### Lambda Web Adapter

Web コンソールのイメージには、Lambda Web Adapter の実行ファイルが `/opt/extensions/lambda-adapter` として入る。バージョンタグと digest は `Dockerfile` で固定してある。これは go.mod の外にある唯一の実行時依存であり、更新は Go モジュールとは別に Dependabot の docker 更新で行う。

アダプタが呼び出しイベントを HTTP へ変換する仕組みと、Web コンソール本体との関係は、[Lambda 実行](../architecture/overview.md#lambda-実行)を参照する。Dependabot による更新方法は、[リリースと依存の更新](release.md)の依存の更新を参照する。

## イメージのテスト

ビルドしたイメージが Lambda ランタイムとして起動することを、デプロイ前に確認する。

```console
make image-test   # 両イメージをビルドし、それぞれ Lambda RIE 上で起動して呼び出す
```

イメージの破損やハンドラの欠落を検出する。実行には Docker が必要であり、エミュレータと使い捨ての state テーブルはテストと一緒に立ち上がる。投入するペイロードと期待する応答は、[テスト](test.md)のイメージのテストを参照する。

## ECR への push

> [!IMPORTANT]
> イメージのプラットフォームは、Lambda 関数のアーキテクチャと一致させること (`arm64` ↔ `linux/arm64`)。不一致のイメージは関数の作成または更新の時点で拒否される。

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
