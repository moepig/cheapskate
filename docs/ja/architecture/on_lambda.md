# Lambda へのデプロイ

reconciler image は Lambda handler として動作する。Web console image は Lambda Web Adapter を介した HTTP server として動作し、API Gateway と接続できる。

reconciler Lambda の timeout は、1 invocation で全設定アイテム、全タグ付きリソースのページ、全 Describe、および全変更 API を処理できる値にする。Lambda timeout の最大値は 900 秒である。cheapskate は checkpoint と分割実行を行わない。

reserved concurrency を 1 にする設定は、任意のコスト最適化として使用できる。正しさは invocation の同時実行を排除することに依存しない。
