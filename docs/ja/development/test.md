# テスト

```console
make unit         # AWS 不要のユニットテスト(go test ./...)
make integration  # `integration` タグ付きテスト。ローカルエミュレータが必要(下記)
make test         # 両方
make lint         # gofmt + go vet(integration タグつき)
```

## 統合テストとローカルエミュレータ

統合テストはローカル AWS エミュレータ **Floci** に対して実行します:

```console
make floci-up     # docker compose up + http://localhost:4566/_localstack/health を待機
make floci-down
```

- 接続の仕組みは `internal/emutest` にあり、標準の環境変数 `AWS_ENDPOINT_URL`(デフォルト `http://localhost:4566`)だけを使います。プロダクションコードはエミュレータの存在を知りません。別の場所のエミュレータを使う場合は `AWS_ENDPOINT_URL` を設定してください。
- エミュレータに到達できない場合、統合テストは失敗ではなく**スキップ**になります。
- Floci の RDS/ECS エミュレーションは実際のコンテナを起動するため、docker ソケットをマウントしています(`compose.yaml` 参照)。

## フィクスチャ

reconciler のテストで使う RDS イベントのサンプルは `internal/reconcile/testdata/` にあります。EventBridge ルールのパターンの参照ペイロードも兼ねています(変更時は `aws events test-event-pattern` で検証してください)。

## モック

テストは [testify](https://github.com/stretchr/testify) のアサーションと [go.uber.org/mock](https://github.com/uber-go/mock)(gomock)のダブルを使います。`internal/store`、`internal/target`、`internal/reconcile` の各インターフェース(定義の隣に `//go:generate` を配置)から `internal/mocks/` に生成されます。`internal/mocks/dynastore.go` だけは手書きで、生成された `MockStoreAPI` をインメモリテーブルに接続し、ストアの実際の Scan/GetItem/PutItem/UpdateItem/DeleteItem と同じ挙動で `Seed`/`Item`/`FailOn`/`SetScanPageSize` を使えるようにしています。

```console
make generate     # go generate ./... — インターフェース変更後に internal/mocks/{store,target,reconcile}.go を再生成
```

生成物はコミットされます(再生成する CI はありません)。モック対象のインターフェースのメソッドを追加・変更したら、必ず `make generate` を実行して差分を含めてください。
