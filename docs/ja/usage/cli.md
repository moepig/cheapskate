# CLI リファレンス

本ドキュメントは、cheapskate-cli のコマンド、オプション、出力形式、終了コード、および設定更新の制約を示す。

## 実行形式と全体オプション

全体オプションは、コマンド名より前に指定する。実行形式を、以下に示す。

```console
cheapskate-cli [-table TABLE] [-output text|json] <command> [arguments]
```

全体オプションと接続先の決定方法を、以下に示す。

| オプション | 既定値 | 用途 |
| --- | --- | --- |
| -table | CHEAPSKATE_TABLE、次に STATE_TABLE_NAME | DynamoDB テーブル名 |
| -output | text | text または json の出力形式 |

AWS の認証情報とリージョンは AWS SDK の設定を使用する。cron の解釈には DEFAULT_TIMEZONE を使用し、未設定時は UTC とする。

## コマンド

コマンドの引数、変更内容、および前提条件を、以下に示す。

| コマンド | 引数 | 動作と前提条件 |
| --- | --- | --- |
| list | なし | グループ設定を一覧表示 |
| show | --group NAME | グループ設定、所属リソースのタグ、AWS の現在状態を表示 |
| schedule | --group NAME -start CRON -stop CRON | グループを作成、または 2 個の cron 属性を更新 |
| override | --group NAME running・stopped・disabled [-for DURATION] | override を設定。無期限ならグループを作成可能。期限付きには既存スケジュールが必要 |
| clear-override | --group NAME | override と有効期限を削除。スケジュールが必要 |
| remove | --group NAME | グループアイテム全体を削除 |

-for は 2h や 30m のような正の期間で指定する。省略すると無期限となる。CLI は AWS リソースの作成、タグ付け、起動・停止を行わない。設定変更の反映は、reconciler の後続サイクルで行う。

コマンドの使用例は、[日常の操作](operations.md)と[CLI によるグループ設定例](cli_records.md)を参照。

## 出力形式

list の text 出力は、有効なグループを stdout、不正なグループを stderr に出力する。json 出力は、一覧の読み取りに成功すると groups と errors を含む 1 個のオブジェクトを stdout に出力する。読み取り自体に失敗した場合は stderr に出力する。

show の json 出力は、group と resources を含む。各リソースの config はタグから得た設定、live は AWS の観測値、live_error は状態取得のエラーである。不正な保存済みグループの場合は、構造化された error オブジェクトを出力する。

CLI の有効期限は DEFAULT_TIMEZONE にかかわらず UTC の RFC 3339 形式で表示する。

## 終了コード

終了コードと判定時の注意を、以下に示す。

| コード | 意味 |
| --- | --- |
| 0 | 成功。show では個別リソースの状態取得エラーを含む場合がある |
| 1 | 引数、変更後の設定検証、AWS、DynamoDB、または内部エラー |
| 2 | 不正な保存済みグループ、または条件付き書き込みの競合 |

list は不正なグループが 1 件以上あれば 2 を返す。show は個別リソースの状態取得に失敗しても 0 を返すため、状態確認では live_error も確認すること。

## 設定更新と競合

schedule、override、および clear-override は、変更後のグループ全体を検証する。schedule は override と ECS 上限を保持し、override と clear-override はスケジュールと ECS 上限を保持する。remove は、属性型などを読み取れるアイテムを、cron や属性間の制約を検証せずに削除する。

条件付き書き込みの競合は自動再試行しない。再読み込み後、意図した変更を明示的に再実行する。異なる属性への変更は互いに保持される。同じ属性への変更は競合として検出されず、DynamoDB で後に適用された値が残る。

> [!WARNING]
> 同名グループの削除・再作成と、別の設定変更を同時に行ってはいけない。書き込み条件は属性の存在を確認するため、古い要求が再作成後のグループを変更・削除する場合がある。
