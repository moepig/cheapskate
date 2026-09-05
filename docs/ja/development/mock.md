# モック

AWS SDK client interface には、`go.uber.org/mock` で生成する typed mock を使用する。interface を変更した場合は、次のコマンドで再生成する。

```console
make generate
```

生成ファイルはリポジトリへ commit する。`internal/state/mocks/dynastore.go` は手書きの in-memory DynamoDB 実装であり、生成した API mock の背後で使用する。store が使用する Query、GetItem、PutItem、UpdateItem、および DeleteItem に加え、条件不一致と Query pagination を再現する。

application port には、`internal/app/port/porttest` の stateful な手書き test double を使用する。検出リソースと観測結果の設定、Start と Stop の記録、および失敗の注入が可能である。application test では、AWS SDK request 型ではなく domain value に注目できる。

assertion には `testify` を使用する。手書き test double には、compile time の interface assertion を記述する。
