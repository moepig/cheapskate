# Reconcile の境界

1 invocation は、リソースを処理する前に 2 個の in-memory snapshot を取得する。全グループに対する強い整合性の Query と、固定タグ検出の全ページである。どちらかの全体読み込みに失敗した場合は、リソースの Describe、Start、および Stop を呼び出さずエラーを返す。

有効な各リソースは個別に処理する。adapter が現在の状態を Describe し、起動・停止状態に差があれば Start または Stop を実行する。遷移中と not-found の観測は処理を見送る。1 リソースの失敗は集計のエラー数へ加算するが、後続リソースは継続する。

ECS adapter は desired count、running count、および pending count がすべて 0 の場合だけ stopped、それ以外は running を返す。停止要求には上下限 0/0 と ScheduledScalingSuspended=true を適用し、設定が不足する停止済み service も再適用する。稼働要求では、起動途中の設定、一時停止要求と AWS フラグの差、および要求が true の場合の上下限の差を再適用する。要求が false の通常稼働中は Scheduled Scaling による上下限変更を維持する。

ECS の初期設定は停止状態、上下限 0/0、または通常のタグ範囲と異なる一時停止中の正の固定範囲から再開する。台数設定が完了するまで Scheduled Scaling を一時停止し、最後に通常の上下限とタグの要求を適用する。稼働中の一時停止設定の再適用は起動台数への再設定を行わない。通常の上下限も起動台数と同じ場合は、フラグの差だけでも復旧できる。最後の更新の応答が失われても設定が一致していれば追加操作を行わない。

アクション履歴と checkpoint は保存しない。同時 invocation は、絶対状態の操作を繰り返したり、異なる設定 snapshot を使用したりする場合がある。このため test は、exactly-once の配送ではなく後続サイクルでの収束を確認する。

アクション通知は、リソース API の成功後に最大 2 回 Publish する。通知失敗はログへ出力するが、リソースへの操作を取り消さず、サイクルも停止しない。

共通サイクルと各リソースの AWS API 呼び出し順は、[リソース操作のシーケンス](resource_sequence.md)を参照。
