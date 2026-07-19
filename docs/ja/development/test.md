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
