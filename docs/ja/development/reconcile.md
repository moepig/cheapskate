# Reconcile の実装契約

本ドキュメントは、サイクル内の読み込み境界、エラー分離、同時実行、およびテストが確認する契約を示す。

## 読み込み境界

1 invocation はリソース処理前に全グループと探索結果の in-memory snapshot を取得する。全グループは DynamoDB の CONFIG パーティションへの強い整合性の Query で取得する。探索では、cheapskate:group タグを持つ対応リソースを Resource Groups Tagging API の GetResources で全ページ取得する。RDS DB クラスタの識別子と所属タグは、RDS の参照 API で照合する。

全体読み込み、ARN 解析、または RDS クラスタの照合に失敗した場合は、エラーを返す。この場合、adapter の Describe、Start、および Stop は呼ばない。

各リソースは 1 サイクルで 1 回処理する。同一 ARN が複数回返された場合は、最後のタグ一式を採用する。RDS クラスタは RDS 応答の DBClusterIdentifier、DBClusterArn、および TagList を採用する。名前と resource ID による検出結果は、同じ ARN へ統合する。この対応関係は探索 1 回の間だけ保持する。

RDS の照会に成功して該当クラスタまたは所属タグが存在しない候補は、元の ARN と理由をログに記録して除外する。

disabled グループのクラスタも、探索時の照合は行う。

## リソース単位の処理

有効なリソースは ARN 順に処理する。グループの要求が disabled の場合は Describe も呼ばない。

adapter の Observation とグループの要求を比較し、起動・停止状態が異なる場合や、要求に対応する NeedsStart・NeedsStop が操作を必要としている場合に操作する。transitioning と not-found は処理を見送る。

グループ・リソース単位の失敗は Summary に集計し、後続リソースを継続する。API の呼び出し順と各 adapter の判定は、[リソース操作のシーケンス](resource_sequence.md)を参照する。

## 状態を保存しない再適用

アクション履歴、checkpoint、および起動フェーズを保存しない。

ECS の Start はサービスと scalable target を再取得して分岐を確認し、Stop は scalable target を再取得する。グループ設定とタグはサイクル冒頭の snapshot を使用する。

同時 invocation は絶対状態の操作を繰り返し、異なる snapshot を適用する場合がある。テストは、古い invocation の終了、設定の安定、および API の成功を前提とする後続サイクルでの収束を確認する。exactly-once の操作を前提にしない。

## 通知境界

アクション通知はリソース API の成功後に SNS の Publish を最大 2 回呼ぶ。通知失敗はログへ出力し、リソース操作を取り消さず、サイクルも停止しない。

応答喪失後の再観測で追加操作が不要なら、操作と通知を再実行しない。

## 検証対象

単体テストでは、次の境界と再適用の契約を確認する。

- 全体読み込みの失敗時に個別処理を開始しないこと
- 個別エラーの分離と各 API 境界の失敗
- 応答喪失とタグ変更後の再適用
- 収束後の追加操作抑止

ECS のテストは、desiredCount と実タスク数の非同期な変化、および Scheduled Scaling による通常範囲変更も扱う。

具体的な検証は、[reconcile テスト](../../../internal/app/reconcile/reconcile_test.go)、[ECS テスト](../../../internal/aws/compute/ecs_test.go)、および[Scheduled Scaling テスト](../../../internal/aws/compute/ecs_scheduled_scaling_test.go)を参照する。
