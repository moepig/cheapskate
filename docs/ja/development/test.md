# テスト

本ドキュメントは、テストの層構成、各層の対象と実行手段、およびテストコードの置き場所を記述する。

実行の入口となる `make` のターゲットを、以下に示す。

```console
make unit         # AWS 不要のユニットテスト(go test ./...)
make integration  # `integration` タグ付きテスト。Docker が必要
make test         # 両方
make image-test   # `image` タグ付きテスト。ビルド済みコンテナイメージを外から叩く
make lint         # gofmt + go vet(integration・image タグつき)+ クロスコンパイル
```

## テストレイヤ

テストは対象の粒度によって 4 層に分かれる。各層の対象と手段を、以下にまとめる。

| レイヤ | 対象 | 手段 |
|---|---|---|
| 単体(`make unit`) | 望ましい状態の解決、設定の検証、収束の判定、ECS のタグ由来の値の復元、ARN のパース | 時刻は引数注入。AWS クライアントは narrow interface + モック |
| 統合(`make integration`) | store の実 DynamoDB 呼び出しと CLI の各コマンド、実アダプタを結線した reconcile 一連動作 | ローカルエミュレータ |
| イメージ(`make image-test`) | ビルド済みイメージが Lambda ランタイムとして起動・応答すること、実イベントのペイロードの扱い、Lambda Web Adapter 越しの応答 | Runtime Interface Emulator |
| 受け入れ(実 AWS) | 7 日自動起動、遷移タイミング、RDS の Stop/Start API、Auto Scaling の実挙動 | dev アカウントへのデプロイ |

受け入れレイヤが独立しているのは、エミュレータが RDS の Stop/Start API と Application Auto Scaling API を再現しないためである。再現できない範囲の全体と、下位レイヤで用いる代替手段は、[../architecture/emulation_local.md](../architecture/emulation_local.md) を参照。

## テストの置き場所

対象が Go のパッケージであれば、そのパッケージの隣に置く。対象が組み上がったものであれば `tests/` に置く。実エミュレータを使うかどうかは判断基準としない。

置き場所ごとの対象とビルドタグの対応を、以下にまとめる。

| 置き場所 | 対象 | タグ |
|---|---|---|
| `internal/**/*_test.go` | そのパッケージの契約。実 DynamoDB を相手にする統合テストもここに置く | なし / `integration` |
| `tests/system/` | reconcile ループに実アダプタを結線したもの。本番で `internal/wire` が行う結線をテスト内で組む | `integration` |
| `tests/image/` | ビルドしたコンテナイメージそのもの | `image` |

`tests/` 配下は動作対象から何も import しない外形テストであり、どのパッケージにも属さない。各ディレクトリの `doc.go`(ビルドタグなし)が位置づけを記述する。

## 統合テスト

ローカル AWS エミュレータに対して実行する。起動は testcontainers-go が自動で行うため、Docker が動作していれば事前準備を要しない。

`make integration` は、パッケージ単位の統合テストと `tests/system/` の両方を実行する。どちらもエミュレータのみを要し、イメージのビルドを要しない。

コンテナは終了時に破棄せず再利用する。削除は `make floci-down` が行い、エミュレータが起動する RDS/ECS コンテナも併せて片付ける。

## イメージのテスト

`make image-test` は、パッケージではなくビルド成果物を対象とする唯一のレイヤである。2 つのイメージをビルドし、ベースイメージ同梱の Runtime Interface Emulator 上で起動して HTTP で呼び出す。

Lambda ハンドラ、JSON の入出力契約、`lambda.norpc` ビルドタグ、および同梱した Lambda Web Adapter が経路に入るのは、このレイヤだけである。RIE は手段であって検証対象ではないため、それを抱えるハーネスは `harness_test.go` に分け、検証内容はイメージごとのファイルに置く。

state テーブルはいずれも使い捨ての空のものを作るため、期待する応答を固定できる。

reconciler に送るペイロードと期待は次のとおりである。

| ペイロード | 期待 |
|---|---|
| `{}` | リソース 0 件の Summary |
| `internal/app/reconcile/testdata/rds-event-*.json` の全件(EventBridge が届けるままの形) | Summary に加えて、コンテナのログにフィクスチャ 1 件につき 1 行の `event-received` |
| `[]`(オブジェクトではないペイロード) | 空のイベントとみなさず、unmarshal で失敗すること |

webconsole に送るペイロードと期待は次のとおりである。

| ペイロード | 期待 |
|---|---|
| API Gateway REST API(v1)の `GET /` プロキシイベント | HTTP 200 のプロキシレスポンス。アダプタが拡張として起動し、イベントをループバック越しの HTTP に組み替え、応答をイベントの応答形式へ戻せていること |
| 同じイベントに、クライアントが `x-amzn-request-context` を詐称して付けたもの | ログに残る `client` がイベントの `requestContext` 由来であり、詐称した IP がログのどこにも現れないこと |

`integration` とは別のタグにしてあるのは、先にイメージをビルドするためである。`Dockerfile` の `COPY . .` により、リポジトリ内のファイルが 1 つでも変われば、レイヤキャッシュは無効になる。

イメージのビルドを testcontainers-go ではなく `docker build` へ委ねているのは、BuildKit の要否によるものである。この判断の根拠は、[../architecture/emulation_local.md](../architecture/emulation_local.md) のイメージビルドの扱いを参照。

## フィクスチャ

RDS イベントのサンプルは `internal/app/reconcile/testdata/` にあり、EventBridge ルールパターンの参照ペイロードを兼ねる。変更時は `aws events test-event-pattern` で検証する。`make image-test` はこのディレクトリを glob するため、フィクスチャを追加すれば自動でイメージに対しても実行される。

## モック

アサーションは testify を使う。テストダブルは、AWS SDK 境界が生成、アプリ層のポートが手書きの 2 本立てである。どちらを選ぶかの基準、生成手順、および手書きダブルの書き方は、[mock.md](mock.md) を参照。
