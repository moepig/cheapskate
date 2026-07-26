# テスト

```console
make unit         # AWS 不要のユニットテスト(go test ./...)
make integration  # `integration` タグ付きテスト。Docker が必要
make test         # 両方
make image-test   # `image` タグ付きテスト。ビルド済みコンテナイメージを外から叩く
make lint         # gofmt + go vet(integration・image タグつき)
```

## テストレイヤ

| レイヤ | 対象 | 手段 |
|---|---|---|
| 単体(`make unit`) | 望ましい状態の解決、設定の検証、収束の判定、ECS のタグ由来の値の復元、ARN のパース | 時刻は引数注入。AWS クライアントは narrow interface + モック |
| 統合(`make integration`) | store の実 DynamoDB 呼び出しと CLI の各コマンド、実アダプタを結線した reconcile 一連動作 | ローカルエミュレータ |
| イメージ(`make image-test`) | ビルド済みイメージが Lambda ランタイムとして起動・応答すること、実イベントのペイロードの扱い、Lambda Web Adapter 越しの応答 | Runtime Interface Emulator |
| 受け入れ(実 AWS) | 7 日自動起動、遷移タイミング、RDS の Stop/Start API、Auto Scaling の実挙動 | dev アカウントへのデプロイ |

エミュレータで再現できない範囲と、その代替は [../architecture/emulation_local.md](../architecture/emulation_local.md)。

## テストの置き場所

対象が Go のパッケージなら、そのパッケージの隣に置く。対象が組み上がったものなら `tests/` に置く。実エミュレータを使うかどうかは判断基準ではない。

| 置き場所 | 対象 | タグ |
|---|---|---|
| `internal/**/*_test.go` | そのパッケージの契約。実 DynamoDB を相手にする統合テストもここに置く | なし / `integration` |
| `tests/system/` | reconcile ループに実アダプタを結線したもの。本番で `internal/wire` が行う結線をテスト内で組む | `integration` |
| `tests/image/` | ビルドしたコンテナイメージそのもの | `image` |

`tests/` 配下は動かす対象から何も import しない外形テストであり、どのパッケージにも属さない。各ディレクトリの `doc.go`(ビルドタグなし)に位置づけを書いてある。

## 統合テスト

ローカル AWS エミュレータに対して実行する。起動は testcontainers-go が自動で行うため、Docker が動いていれば事前準備を要しない。

`make integration` は、パッケージ単位の統合テストと `tests/system/` の両方を実行する。どちらもエミュレータのみを要し、イメージのビルドを要しない。

コンテナは終了時に破棄せず再利用する。削除は `make floci-down` が行い、エミュレータが起動する RDS/ECS コンテナも併せて片付ける。

## イメージのテスト

`make image-test` は、パッケージではなくビルド成果物を対象とする唯一のレイヤである。2 つのイメージをビルドし、ベースイメージ同梱の Runtime Interface Emulator 上で起動して HTTP で呼び出す。

Lambda ハンドラ、JSON の入出力契約、`lambda.norpc` ビルドタグ、同梱した Lambda Web Adapter が経路に入るのはこのレイヤだけである。RIE は手段であって検証対象ではないため、それを抱えるハーネスは `harness_test.go` に分け、何を検証するかはイメージごとのファイルに置く。

state テーブルはどちらも使い捨ての空のものを作るため、期待する応答を固定できる。

reconciler に送るペイロードと期待:

| ペイロード | 期待 |
|---|---|
| `{}` | リソース 0 件の Summary |
| `internal/app/reconcile/testdata/rds-event-*.json` の全件(EventBridge が届けるままの形) | Summary に加えて、コンテナのログにフィクスチャ 1 件につき 1 行の `event-received` |
| `[]`(オブジェクトではないペイロード) | 空のイベントとみなさず、unmarshal で失敗すること |

webconsole に送るペイロードと期待:

| ペイロード | 期待 |
|---|---|
| API Gateway REST API(v1)の `GET /` プロキシイベント | HTTP 200 のプロキシレスポンス。アダプタが拡張として起動し、イベントをループバック越しの HTTP に組み替え、応答をイベントの応答形式へ戻せていること |
| 同じイベントに、クライアントが `x-amzn-request-context` を詐称して付けたもの | ログに残る `client` がイベントの `requestContext` 由来であり、詐称した IP がログのどこにも現れないこと |

`integration` とは別のタグにしてあるのは、先にイメージをビルドするためである。`Dockerfile` の `COPY . .` により、リポジトリ内のファイルが 1 つでも変わればレイヤキャッシュは無効になる。

ビルドを `docker build` へ委ねている理由は [../architecture/emulation_local.md](../architecture/emulation_local.md)。

## フィクスチャ

RDS イベントのサンプルは `internal/app/reconcile/testdata/` にあり、EventBridge ルールパターンの参照ペイロードを兼ねる。変更時は `aws events test-event-pattern` で検証する。`make image-test` はこのディレクトリを glob するため、フィクスチャを追加すれば自動でイメージに対しても流れる。

## モック

アサーションは testify を使う。テストダブルは AWS SDK 境界が生成、アプリ層のポートが手書きの 2 本立てである。使い分けの基準と書き方は [mock.md](mock.md)。
