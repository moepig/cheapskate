# cheapskate-cli のアーキテクチャ

グループ設定を操作する CLI。操作の実態は DynamoDB のアイテム CRUD であり、インタフェースは意図ベースの動詞で与える。

## アクセス範囲

| 対象 | 操作 |
|---|---|
| DynamoDB state テーブル | `group#`/`override#` の読み書きと削除、`status#` の読み取りと孤立レコードの削除 |
| Resource Groups Tagging API | `tag:GetResources`(読み取り専用) |
| RDS / ECS / EC2 | `Describe*`(読み取り専用) |

制御系 API は呼ばない。

## レイヤ構成

| パッケージ | 役割 |
|---|---|
| `internal/app/groups` | 設定の検証と store 呼び出し。Web コンソールと共用 |
| `internal/app/doctor` | state テーブルの診断と孤立レコードの削除。Web コンソールと共用 |
| `internal/state` | DynamoDB アクセス層。reconciler と共用 |
| `internal/aws/tagging` | 検出。reconciler と同じ `Discoverer` |

CLI 固有のデータアクセスコードは持たない。

## コマンドとアイテム操作の対応

| コマンド | 実態 | 検出 |
|---|---|---|
| `set-selector` | `group#` のセレクタを設定する。グループが無ければ `mode: disabled` で作成する | 行わない |
| `pin` / `unpin` / `schedule` / `disable` | `group#` の `mode` と関連属性を更新する | 行わない |
| `override` / `clear-override` | `override#` の PUT(TTL 付き)と DELETE | 行わない |
| `list` | テーブル全体の 1 回の Scan | 行わない |
| `show` | 1 グループの設定 + override + 検出したリソース(状態付き) | 対象グループのみ |
| `remove` | グループの `group#`/`override#`/`status#group#` を削除する | 行わない |
| `doctor` | テーブルの不整合を診断する。`--prune` を付けたときだけ孤立レコードを削除する | 全グループ |

`doctor` が全グループを検出する理由と、削除の対象範囲は [overview.md](overview.md)。

## 出力

出力は JSON に統一する。全コマンドが stdout に JSON オブジェクトを 1 つだけ出力し、人間向けの整形テキストを持たない。

| 経路 | 出力 | 終了コード |
|---|---|---|
| 成功 | stdout に 1 オブジェクト。`command` フィールドがコマンドを示す | 0 |
| 失敗 | stderr に `{"error": "..."}` | 1 |
| 検出の失敗(`show`) | stdout に `discover_error` フィールドを含む 1 オブジェクト | 0 |
| usage(`-h` / `-help`) | stdout にテキスト | 0 |

`flag` パッケージ自身のエラー出力は破棄しており、フラグの誤用も同じ JSON エラーになる。

DynamoDB アイテムの形をそのまま出さず、CLI の出力用の型へ詰め替える。ストレージ都合の `pk` を出さず、セレクタは 3 つの属性ではなく 1 つのオブジェクトとして、`expires_at` は epoch 秒ではなく RFC3339 UTC として出力する。

## 検証

書き込み前に、`desired` の値、cron 式、タイムゾーン、セレクタ(`aws:` で始まるタグキーの拒否、既知タイプの部分集合であること、タグ値が空でないこと)を検証する。

## 接続

| 項目 | 与え方 |
|---|---|
| テーブル名 | `-table` フラグまたは環境変数 `CHEAPSKATE_TABLE` |
| 認証・リージョン・エンドポイント | AWS SDK の標準チェーン。`AWS_ENDPOINT_URL` でローカルエミュレータに接続できる |
| tzdata | バイナリに埋め込む |

## IaC との使い分け

`group#` アイテムは IaC で管理できる。reconciler は `group#` に書き込まないため、ドリフトしない。`override#` は TTL で失効するため IaC 管理の対象にしない。恒久設定は IaC または `cheapskate-cli`、一時操作は override が担う。
