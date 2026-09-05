# テスト

主な test target を次に示す。

```console
make unit
make integration
make test
make image-test
make lint
```

unit test は、グループ不変条件、cron の到達可能性、望ましい状態の決定、pagination、条件付き書き込み、adapter の検証、reconcile の開始前 barrier とエラー分離、CLI 出力、および Web security を確認する。

integration test は、ローカル AWS emulator を使用して実 DynamoDB expression と schedule/override の lifecycle を確認する。system test は、schedule による Stop、期限付き running override、失効後の schedule 復帰、disabled による抑止、古い読み取り後の書き込み競合、および別属性への同時変更を扱う。

image test は両 container image を build する。reconciler には任意の JSON payload を渡し、full cycle が完了することを確認する。Web test は Lambda Web Adapter の経路と request metadata の処理を確認する。起動 test は UTC の既定値と不正な `DEFAULT_TIMEZONE` を扱う。

AWS SDK の境界には生成 mock、application port には手書き test double を使用する。詳細は、[モック](mock.md)を参照すること。
