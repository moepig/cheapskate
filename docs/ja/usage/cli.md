# CLI リファレンス

本ドキュメントは、cheapskate-cli のコマンド、オプション、出力形式、終了コード、および設定更新の制約を示す。

## 実行形式と全体オプション

全体オプションは、コマンド名より前に指定する。実行形式を、以下に示す。

```console
cheapskate-cli [-config PATH] [-context NAME] [-table TABLE] [-output text|json] <command> [arguments]
```

全体オプションと接続先の決定方法を、以下に示す。

| オプション | 既定値 | 用途 |
| --- | --- | --- |
| -config | CHEAPSKATE_CONFIG、次に OS ごとの既定パス | CLI 設定ファイルのパス |
| -context | 保存済みの選択中 context | 今回の実行で使う context。保存済みの選択は変更しない |
| -table | 選択中 context、次に CHEAPSKATE_TABLE、STATE_TABLE_NAME | DynamoDB テーブル名 |
| -output | text | text または json の出力形式 |

AWS profile、リージョン、およびスケジュールのタイムゾーンには、選択中 context の設定を使用する。未設定の項目については、AWS の認証情報とリージョンは AWS SDK の設定、タイムゾーンは DEFAULT_TIMEZONE を使用する。タイムゾーンの既定値は UTC である。context の設定は環境変数より優先し、-table は context のテーブル名より優先する。

## context

context は、接続設定を名前でまとめたものである。context と選択状態を JSON ファイルに保存し、次回以降の実行でも使用する。config コマンドはローカルで動作し、AWS の認証情報や環境変数によるテーブル名の指定を必要としない。

context の管理コマンドを、以下に示す。

| コマンド | 引数 | 動作 |
| --- | --- | --- |
| config set-context | NAME --table TABLE [--profile PROFILE] [--region REGION] [--timezone ZONE] | context を作成・更新。省略した項目は保存済みの値を保持 |
| config use-context | NAME | 選択状態を保存。登録済みの context が必要 |
| config get-contexts | なし | 名前順で一覧表示。選択中の context に * を付ける |
| config current-context | なし | 保存済みの選択を表示。未選択時はエラー |

新しい context には --table が必須である。任意項目は AWS 共有設定の profile 名、AWS リージョン、およびスケジュールの IANA タイムゾーンである。任意項目に空文字列を指定すると、その設定を削除する。set-context は選択状態を変更しない。認証情報は AWS SDK の設定で管理し、CLI は profile 名を保存する。

context の登録・選択と、一時的に別の context を使う例を、以下に示す。

```console
cheapskate-cli config set-context dev --table cheapskate-dev --profile dev --region ap-northeast-1 --timezone Asia/Tokyo
cheapskate-cli config set-context prod --table cheapskate-prod --profile prod --region ap-northeast-1 --timezone Asia/Tokyo
cheapskate-cli config use-context dev
cheapskate-cli list
cheapskate-cli -context prod list
cheapskate-cli config current-context
```

-context はその実行だけに適用するため、最後のコマンドは dev を表示する。スケジュールのタイムゾーンは reconciler と Web コンソールに合わせること。

既定の保存先を、以下に示す。

| OS | パス |
| --- | --- |
| Linux | $XDG_CONFIG_HOME/cheapskate/config.json。未設定時は ~/.config/cheapskate/config.json |
| macOS | ~/Library/Application Support/cheapskate/config.json |
| Windows | %AppData%\cheapskate\config.json |

-config は CHEAPSKATE_CONFIG より優先する。ファイルが存在しない場合は保存済み context がない状態として扱い、context または選択状態の保存時に作成する。不正なファイルはエラーとなる。ファイルは current_context と、context 名をキーとする contexts オブジェクトを含む。各 context のフィールドは table、profile、region、timezone である。

-output json を指定した場合、get-contexts は CLI 設定オブジェクト、set-context は context 名の name と保存済みの context のフィールド、use-context と current-context は current_context を含むオブジェクトを返す。

## コマンド

コマンドの引数、変更内容、および前提条件を、以下に示す。

| コマンド | 引数 | 動作と前提条件 |
| --- | --- | --- |
| list | なし | グループ設定を一覧表示 |
| show | --group NAME | グループ設定、所属リソースのタグ、AWS の現在状態を表示 |
| schedule | --group NAME -start CRON -stop CRON | グループを作成、または 2 個の cron 属性を更新 |
| override | --group NAME running・stopped・disabled [-for DURATION] | override を設定。無期限ならグループを作成可能。期限付きには既存スケジュールが必要 |
| clear-override | --group NAME | override と有効期限を削除。スケジュールが必要 |
| ecs-max-count | --group NAME COUNT | 既存グループの ecs_max_count を登録・変更。COUNT は 1～2147483647 の整数 |
| clear-ecs-max-count | --group NAME | 既存グループの ecs_max_count 属性を削除 |
| remove | --group NAME | グループアイテム全体を削除 |

-for は 2h や 30m のような正の期間で指定する。省略すると無期限となる。CLI は AWS リソースの作成、タグ付け、起動・停止を行わない。設定変更の反映は、reconciler の後続サイクルで行う。

ecs-max-count はグループを新規作成しない。先に schedule または無期限 override で作成する。clear-ecs-max-count は 0 を保存せず属性を削除し、現在のリソースを停止しない。ECS の起動と稼働中の設定更新には上限の再登録が必要となる。DynamoDB のグループ設定項目は、すべて CLI から登録できる。

コマンドの使用例は、[日常の操作](operations.md)と[CLI によるグループ設定例](cli_records.md)を参照。

## 出力形式

list の text 出力は、有効なグループを stdout、不正なグループを stderr に出力する。json 出力は、一覧の読み取りに成功すると groups と errors を含む 1 個のオブジェクトを stdout に出力する。読み取り自体に失敗した場合は stderr に出力する。

show の json 出力は、グループ設定の group と、所属リソースの配列 resources を含む。resources の各要素の type は cheapskate のリソースタイプ名、ref はリソース固有の識別子、arn はリソースの ARN である。各要素の config はタグから得た設定、live は AWS の観測値、live_error は状態取得のエラーである。不正な保存済みグループの場合は、構造化された error オブジェクトを出力する。

CLI では、override の有効期限をスケジュールのタイムゾーンにかかわらず UTC の RFC 3339 形式で表示する。

## 終了コード

終了コードと判定時の注意を、以下に示す。

| コード | 意味 |
| --- | --- |
| 0 | 成功。show では個別リソースの状態取得エラーを含む場合がある |
| 1 | 引数、変更後の設定検証、AWS、DynamoDB、または内部エラー |
| 2 | 不正な保存済みグループ、または条件付き書き込みの競合 |

list は不正なグループが 1 件以上あれば 2 を返す。show は個別リソースの状態取得に失敗しても 0 を返すため、状態確認では live_error も確認すること。

## 設定更新と競合

schedule、override、clear-override、ecs-max-count、および clear-ecs-max-count は、変更後のグループ全体を検証する。schedule は override と ECS 上限を保持し、override と clear-override はスケジュールと ECS 上限を保持する。ecs-max-count と clear-ecs-max-count は ECS 上限だけを変更し、スケジュールと override を保持する。remove は、属性型などを読み取れるアイテムを、cron や属性間の制約を検証せずに削除する。

条件付き書き込みの競合は自動再試行しない。再読み込み後、意図した変更を明示的に再実行する。異なる属性への変更は互いに保持される。同じ属性への変更は競合として検出されず、DynamoDB で後に適用された値が残る。

> [!WARNING]
> 同名グループの削除・再作成と、別の設定変更を同時に行ってはいけない。書き込み条件は属性の存在を確認するため、古い要求が再作成後のグループを変更・削除する場合がある。
