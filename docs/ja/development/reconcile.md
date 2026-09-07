# Reconcile の境界

1 invocation は、リソースを処理する前に 2 個の in-memory snapshot を取得する。全グループに対する強い整合性の Query と、固定タグ検出の全ページである。どちらかの全体読み込みに失敗した場合は、リソースの Describe、Start、および Stop を呼び出さずエラーを返す。

有効な各リソースは個別に処理する。adapter が現在の状態を Describe し、起動・停止状態に差があれば Start または Stop を実行する。遷移中と not-found の観測は処理を見送る。ECS adapter はタスク数がすべて 0 の場合だけ stopped、それ以外は running を返し、状態が一致していても scalable target の上下限に差があれば再適用を要求する。1 リソースの失敗は集計のエラー数へ加算するが、後続リソースは継続する。

アクション履歴と checkpoint は保存しない。同時 invocation は、絶対状態の操作を繰り返したり、異なる設定 snapshot を使用したりする場合がある。このため test は、exactly-once の配送ではなく後続サイクルでの収束を確認する。

アクション通知は、リソース API の成功後に最大 2 回 Publish する。通知失敗はログへ出力するが、リソースへの操作を取り消さず、サイクルも停止しない。
