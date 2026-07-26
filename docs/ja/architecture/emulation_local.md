# ローカルエミュレーションのアーキテクチャ

ローカル開発とテストは、ローカル AWS エミュレータ Floci 上で行う。

## 接続方式

接続の切り替えは、AWS SDK が標準で解釈する環境変数 `AWS_ENDPOINT_URL` のみで行う。プロダクションコードにエンドポイントの分岐やテスト用フックを持たない。

## Floci

LocalStack Community 互換のエミュレータである。LocalStack Community の認証トークン必須化と更新凍結に伴い採用した。

| 項目 | 値 |
|---|---|
| エンドポイント | `http://localhost:4566` |
| ヘルスチェック | `/_localstack/health` |
| docker ソケット | マウントする。RDS/ECS のエミュレーションが実コンテナを起動するため |

起動経路は 2 つあり、どちらも同じエンドポイントを提供する。

| 経路 | 起動主体 | 用途 |
|---|---|---|
| `compose.yaml` | docker compose | ソースから各コンポーネントを起動する場合 |
| testcontainers-go | テストバイナリ | 統合テストとイメージテスト |

### コンテナのライフサイクル

接続ヘルパは `internal/devtools/emutest` にある。testcontainers-go で固定名のコンテナを 1 つだけ起動し、並走する全テストバイナリと次回以降の実行がそれを再利用する。テストが作るリソースはすべて名前空間分離されているため、共有して問題ない。

Ryuk リーパーは無効化してある。`go test ./...` はパッケージごとに別バイナリを走らせるが testcontainers のセッションは全バイナリで 1 つであり、Ryuk はクライアント接続数が 0 になった時点でそのセッションのコンテナを一括削除する。すなわち、最初に終わったバイナリが、まだ走っている他バイナリのエミュレータまで消してしまう。削除は明示的に行う。

### 対応していない範囲と代替

| 範囲 | 代替 |
|---|---|
| RDS の Stop/Start API | `tests/system` は Describe のみ実クライアントを使い、Stop/Start はスパイに置き換える。実呼び出しは受け入れ試験で検証する |
| `tag:GetResources`(限定的) | `tests/system` は検出をスタブに置き換える。Floci に対して行うのは DynamoDB の読み書きのみである |
| Application Auto Scaling API | ECS の start/stop は Floci 上では必ず失敗する。単体テストは gomock、実呼び出しは受け入れ試験で検証する |
| 遷移中状態(starting/stopping) | 遷移中状態を返すフェイクによる単体テストで検証する |

## state テーブル

スキーマ(`pk` ハッシュキー + `expires_at` TTL)の Go 定義は `internal/state` のみが持つ。ローカルでの作成は `cmd/dev-bootstrap` が行い、これは冪等であり Lambda イメージに含まれない。本番テーブルは利用者の IaC が同じスキーマで作成する。

## ダミーリソース

`internal/devtools/devseed` が ECS クラスタとサービスを作成する。セレクタに一致するもの、パラメータのタグを持つもの、一致しないものをそれぞれ含み、グループページと `show` の表示を確認できる。

RDS/EC2 のダミーリソースは持たない。Tagging API の対応も限定的であるため、ECS 以外の種別は空のリソース一覧または検出エラーとなる。

## Lambda ランタイムのローカル実行

ビルド済みイメージを Lambda として動かす場合は、ベースイメージ同梱の Runtime Interface Emulator を使う。外形的な振る舞いと本番との差は [on_lambda.md](on_lambda.md)。

コンテナの起動と後片付けは testcontainers-go が行う。エミュレータと使い捨ての state テーブルも `emutest` 経由でテストと一緒に立ち上がり、コンテナからエミュレータへの到達には `host.docker.internal` を使う。どちらの起動経路のエミュレータでも、ホストにポートが公開されていれば届く。

ビルドにプラットフォームは指定しない。RIE はホストの docker でコンテナを動かすため、既定のホストアーキテクチャ向けビルドが正しい。

### イメージのビルドを docker CLI に委ねる理由

testcontainers-go のイメージビルドは旧 `/build` API を通るため、BuildKit 前提のこの `Dockerfile` を解釈できない。BuildKit を選ぶ指定を加えても、セッションを張るのは docker CLI の仕事であるため、クライアントライブラリ単体では失敗する。
