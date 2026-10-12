# Reconcile の実装契約

本ドキュメントは、サイクル内の読み込み境界、エラー分離、同時実行、およびテストが確認する契約を示す。

## 読み込み境界

1 invocation は、リソース処理前に全グループと探索結果の in-memory snapshot を取得する。全グループは CONFIG パーティションへの強い整合性の Query、探索は固定タグを持つ対応リソースへの GetResources の全ページである。

どちらかの全体読み込みまたは ARN 解析に失敗した場合は、adapter の Describe、Start、および Stop を呼ばずエラーを返す。同一 ARN が複数回返された場合は、最後のタグ一式を採用し、1 サイクルで 1 回処理する。

## リソース単位の処理

有効なリソースは ARN 順に処理する。disabled は Describe も呼ばない。adapter の Observation とグループの要求を比較し、起動・停止状態または NeedsStart・NeedsStop に差があれば操作する。transitioning と not-found は処理を見送る。

グループ・リソース単位の失敗は Summary に集計し、後続リソースを継続する。API の呼び出し順と各 adapter の判定は、[リソース操作のシーケンス](resource_sequence.md)を参照。

## 状態を保存しない再適用

アクション履歴、checkpoint、および起動フェーズを保存しない。ECS の Start はサービスと scalable target を再取得して分岐を確認し、Stop は scalable target を再取得する。グループ設定とタグはサイクル冒頭の snapshot を使用する。

同時 invocation は絶対状態の操作を繰り返し、異なる snapshot を適用する場合がある。テストは、古い invocation の終了、設定の安定、および API の成功を前提とする後続サイクルでの収束を確認する。exactly-once の操作を前提にしない。

## 通知境界

アクション通知はリソース API の成功後に最大 2 回 Publish する。通知失敗はログへ出力し、リソース操作を取り消さず、サイクルも停止しない。応答喪失後の再観測で追加操作が不要なら、操作・通知を再実行しない。

## 検証対象

単体テストでは、読み込み前の全体中断、個別エラーの分離、各 API 境界の失敗、応答喪失、タグ変更、および収束後の追加操作抑止を確認する。ECS では desiredCount と実タスク数の非同期な変化、および Scheduled Scaling による通常範囲変更も扱う。

具体的な検証は、[reconcile テスト](../../../internal/app/reconcile/reconcile_test.go)、[ECS テスト](../../../internal/aws/compute/ecs_test.go)、および[Scheduled Scaling テスト](../../../internal/aws/compute/ecs_scheduled_scaling_test.go)を参照。
