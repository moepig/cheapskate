# テスト

本ドキュメントは、自動テストの実行方法、検証対象、および検証範囲の制限を示す。

## 実行するテストを選ぶ

テストを実行する主な make のターゲットを次に示す。

```console
make unit
make integration
make test
make image-test
make lint
```

| ターゲット | 検証対象 |
| --- | --- |
| `make unit` | Go の単体テスト |
| `make integration` | エミュレータを使う結合テスト |
| `make test` | 単体テストと結合テスト |
| `make image-test` | 両イメージの起動と呼び出し |
| `make lint` | fmt、vet、CLI のクロスコンパイル |

## 単体テスト

単体テストは、ドメイン、状態ストア、adapter、reconcile、CLI、および Web の動作を検証する。

- ドメイン: グループ不変条件、cron の到達可能性、望ましい状態の決定
- 状態ストア: pagination と条件付き書き込み
- adapter: 入力と操作の検証
- reconcile: 全体読み込み完了前の個別処理の抑止とエラー分離
- CLI: 出力
- Web: セキュリティ上の動作

Aurora の探索では、すべての探索読み込みに成功するまでリソースの状態取得・操作と通知を開始しないことを検証する。複数クラスタと各エイリアスの混在、アカウント・リージョンを越えた対応関係の再利用の拒否、および一部のクラスタ照合に成功した後の失敗を確認する。

AWS SDK の境界の単体テストには生成 mock、application port には手書き test double を使用する。詳細は[モック](mock.md)を参照する。

## AWS SDK の接続試験

SDK の接続試験は本体の依存関係を組み立てる処理から実際の AWS SDK client を生成し、ローカル HTTP サーバーへ接続する。Tagging API の JSON、RDS の XML、署名付きリクエスト、resource ID フィルタ、ページ継続、および AccessDenied の伝播を確認する。

## 結合テスト

結合テストはローカル AWS エミュレータを使用して実 DynamoDB expression と schedule/override のライフサイクルを確認する。

system test は schedule/override と同時書き込みの次の条件を扱う。

- schedule による Stop
- 期限付き running override
- override 失効後の schedule 復帰
- disabled による抑止
- 古い読み取り後の書き込み競合
- 別属性への同時変更

RDS の結合テストは Floci に複数の Aurora クラスタを作成し、テスト終了時に削除する。

固定した [Floci 1.7.0 の RDS 実装](https://github.com/floci-io/floci/blob/75c8cfc4d93b62063b6a435dff0fc77df180ff84/src/main/java/io/github/hectorvent/floci/services/rds/RdsQueryHandler.java)は、クラスタの TagList を返さず、db-cluster-resource-id フィルタにも対応しない。結合テストでは、所属タグを確認できない応答の除外と、識別子で絞り込まれていない応答による探索の中断を検証する。AWS 形式の成功応答はローカル HTTP サーバーによる接続試験で扱う。

実 AWS への接続と IAM ポリシー自体の評価は行わない。

## イメージのテスト

イメージテストは両コンテナイメージをビルドして呼び出す。reconciler には任意の JSON payload を渡し、サイクル全体が完了することを確認する。Web のテストは Lambda Web Adapter の経路と request metadata の処理を確認する。

起動テストは、タイムゾーンの既定値が UTC であることと、不正な `DEFAULT_TIMEZONE` の扱いを確認する。
