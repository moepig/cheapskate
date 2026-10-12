# トラブルシューティング

本ドキュメントは、設定保存、起動・停止、ECS スケーリング、および監視で期待した結果が得られない場合の確認項目と対処を示す。

## 最初に確認する情報

CLI の show または Web コンソールで、グループ設定、リソースの所属タグ、現在状態、および live_error を確認する。reconciler のログでは、summary のエラー数と、該当する group-error・resource-error を確認する。確認方法は、[ログ・メトリクス・通知](monitoring.md)を参照。

## 設定保存のエラー

主な症状と対処を、以下に示す。

| 症状 | 確認と対処 |
| --- | --- |
| 不正なグループとして表示される | 未知属性、DynamoDB の属性型、cron の組、override 値、有効期限を確認 |
| 期限付き override を保存できない | 両方の cron を登録し、期限を未来に設定 |
| 失効後にスケジュールを変更できない | clear-override で失効した override を解除するか、新しい override に置き換える |
| override を解除できない | スケジュールを追加してから解除。override だけのグループは別の override に置き換えるか、管理終了後に削除 |
| HTTP 409 または書き込み時の CLI 終了コード 2 | 再読み込みして設定を確認し、意図した変更を明示的に再実行 |
| Web コンソールが HTTP 403 を返す | 転送される Host と ALLOWED_HOSTS、フォーム送信元の Origin を確認 |

値と属性の制約は、[グループ設定](group_config.md)を参照。CLI と Web コンソールで修復できないアイテムはバックアップし、意図的に削除・再作成する。同名グループの削除・再作成と他の設定変更を同時に行わないこと。

## 起動・停止の未反映

グループ設定と AWS の状態を比較し、次の点を確認する。

| 確認項目 | 対処 |
| --- | --- |
| disabled override が有効 | 必要な running・stopped override に置き換えるか、スケジュールに戻す |
| 計算されるスケジュールが想定と異なる | DEFAULT_TIMEZONE と直近の起動・停止時刻を確認 |
| リソースが一覧に現れない | cheapskate:group と Resource Groups Tagging API の検出結果を確認 |
| 所属先が未登録または不正 | タグ値と DynamoDB のグループ名を一致させる |
| リソースの状態取得・操作エラー | 対応する IAM 権限、AWS API のエラー、throttling を確認 |
| RDS・EC2 が遷移中 | 後続サイクルまで待ち、AWS 側の遷移結果を確認 |
| リソース種別・構成が対象外 | 対応条件を満たすリソースを登録 |

反映タイミングと対象条件は、[起動・停止の挙動](resource_flow.md)を参照。

## ECS の台数・一時停止の相違

ECS の設定と現在状態は別の値である。症状ごとの確認項目を、以下に示す。

| 症状 | 確認と対処 |
| --- | --- |
| 起動または設定修復がエラーになる | ecs_max_count の登録、タグ値の型、通常の上下限と起動台数の関係、グループ上限の超過を確認 |
| タグの起動台数へ変わらない | cheapskate/desired-count は停止状態または上下限 0/0 からの起動時だけ適用 |
| runningCount が desiredCount と異なる | タスクの起動・終了は非同期。AWS のイベントと後続サイクルを確認 |
| Scheduled Scaling の再開時に台数が変わる | 通常の上下限を適用した結果、範囲外の desiredCount が調整されていないか確認 |
| 上下限が通常のタグ値と異なる | 一時停止要求が false なら Scheduled Scaling による変更を維持。タグ範囲を維持するなら true を指定 |
| 停止中に一時停止を解除できない | グループ要求が stopped の間は ScheduledScalingSuspended=true を維持 |
| Scheduled Scaling で 0 台にしても再起動する | 0 台を維持する時間帯は cheapskate の停止スケジュールまたは stopped override を使用 |

設定値は、[リソースタグ](resource_tag.md#ecs-設定タグ)と[グループ設定](group_config.md#ecs-台数上限)を参照。具体的な状態の変化は、[ECS の起動・停止とスケーリング](ecs.md#設定と現在状態の具体例)を参照。

## サイクル全体の中断

Lambda 呼び出しがエラーになり summary が出ない場合は、DynamoDB の読み込みまたはリソース検出のエラーを確認する。dynamodb:Query、tag:GetResources の権限と到達性、およびログに示された ARN の形式を確認する。全体の読み込みに失敗したサイクルでは、個別リソースの変更は始まらない。

## 通知・メトリクスの欠落

通知先の NOTIFICATION_TOPIC_ARN、SNS 権限、subscription を確認する。操作が成功していても通知だけが失敗する場合がある。通知の重複・欠落とメトリクスの出力条件は、[ログ・メトリクス・通知](monitoring.md)を参照。
