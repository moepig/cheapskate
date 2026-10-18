# テスト

テストを実行する主な make のターゲットを次に示す。

```console
make unit
make integration
make test
make image-test
make lint
```

unit test は、グループ不変条件、cron の到達可能性、望ましい状態の決定、pagination、条件付き書き込み、adapter の検証、reconcile の開始前 barrier とエラー分離、CLI 出力、および Web security を確認する。

Aurora の探索では、複数クラスタと各エイリアスの混在、アカウント・リージョンを越えた対応関係の再利用の拒否、および一部のクラスタ照合に成功した後の失敗を確認する。探索が完了するまで、すべてのリソースの状態取得・操作と通知を開始しないことを検証する。

SDK の接続試験は、合成ルートから実際の AWS SDK をローカル HTTP サーバーへ接続する。Tagging API の JSON、RDS の XML、署名付きリクエスト、resource ID フィルタ、ページ継続、および AccessDenied の伝播を確認する。

integration test は、ローカル AWS emulator を使用して実 DynamoDB expression と schedule/override の lifecycle を確認する。system test は、schedule による Stop、期限付き running override、失効後の schedule 復帰、disabled による抑止、古い読み取り後の書き込み競合、および別属性への同時変更を扱う。

RDS の結合テストは Floci に複数の Aurora クラスタを作成し、テスト終了時に削除する。固定した [Floci 1.7.0 の RDS 実装](https://github.com/floci-io/floci/blob/75c8cfc4d93b62063b6a435dff0fc77df180ff84/src/main/java/io/github/hectorvent/floci/services/rds/RdsQueryHandler.java)は、クラスタの TagList を返さず、db-cluster-resource-id フィルタにも対応しない。結合テストでは、所属タグを確認できない応答の除外と、識別子で絞り込まれていない応答による探索の中断を検証する。AWS 形式の成功応答はローカル HTTP サーバーによる接続試験で扱う。実 AWS への接続と IAM ポリシー自体の評価は行わない。

image test は両 container image を build する。reconciler には任意の JSON payload を渡し、full cycle が完了することを確認する。Web test は Lambda Web Adapter の経路と request metadata の処理を確認する。起動 test は UTC の既定値と不正な `DEFAULT_TIMEZONE` を扱う。

AWS SDK の境界の単体テストには生成 mock、application port には手書き test double を使用する。詳細は、[モック](mock.md)を参照すること。
