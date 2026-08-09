# アーキテクチャ概要

## 設計原則

本ドキュメントが扱う実装は、次の 4 つの原則に基づく。各原則の内容を、以下に示す。

| 原則 | 内容 |
|---|---|
| 望ましい状態 + reconcile ループ | 命令の実行ではなく状態への収束として動作する。遷移の待ち合わせを持たず、遷移中のリソースはスキップして次サイクルで再試行する |
| 責務分離 | RDS/ECS/EC2 の制御系 API を呼ぶのは reconciler のみ。`cheapskate-cli` と Web コンソールがアクセスするのは DynamoDB と読み取り専用の `tag:GetResources` に限られる |
| AWS タグによる動的検出 | グループは設定とセレクタ(タグのキー/値 + 対象リソースタイプ)を持ち、メンバーシップは reconcile ごとに Resource Groups Tagging API で解決する。真実源は AWS タグの現在値であり、リソースの個別登録は存在しない |
| 配布物はイメージとバイナリに限る | リリースごとにコンテナイメージを GHCR へ、CLI のアーカイブを GitHub リリースへ公開する。IaC テンプレートは配布せず、AWS リソースの作成手段を規定しない |

## 構成要素

実行ファイルは 3 つある。それぞれの実行環境と役割を、以下に示す。

| 構成要素 | 実行環境 | 役割 |
|---|---|---|
| reconciler | Lambda | reconcile ループ。AWS リソースを起動・停止する唯一のコンポーネントである |
| `cheapskate-cli` | 手元の端末 | グループ設定の投入・確認と state テーブルの診断。AWS へのデプロイを要しない |
| Web コンソール | Lambda(任意) | CLI と同じ操作をブラウザから行う。デプロイしなくても reconcile ループは完結する |

3 つは互いを呼び出さない。関係するのは state テーブルを介してのみであり、reconciler が書くアイテムと設定側が書くアイテムは重ならない。

`cheapskate-cli` の設計は [cheapskate-cli.md](cheapskate-cli.md)、Web コンソールの設計は [web_console.md](web_console.md) を参照。

## データモデル

state は DynamoDB テーブル 1 つに保持する。アイテムは 3 種類であり、それぞれの内容と書き手を、以下に示す。

| アイテム | 保持する内容 | 書き手 |
|---|---|---|
| `group#<名前>` | グループの望ましい状態の決め方とセレクタ | `cheapskate-cli` / Web コンソール / IaC |
| `override#<名前>` | 期限付きで望ましい状態を上書きする指定 | 同上 |
| `status#<...>` | reconciler の実行結果(直近のアクション、直近のエラー、継続中の遷移) | reconciler |

設定側が書くアイテムと reconciler が書くアイテムは重ならない。この分離により、`group#` を IaC で管理しても reconciler の書き込みとドリフトしない。詳細は、[database.md](database.md) のキー配置、属性、および読み書きマトリクスを参照。

## 望ましい状態の解決

グループ設定と override から、そのグループの望ましい状態を決める。判定の優先順位を、以下に示す。上の行を優先する。

| 優先順位 | 条件 | 決まる状態 |
|---|---|---|
| 1 | `mode: disabled` | 望ましい状態を決めず、そのグループを処理対象から外す |
| 2 | 期限内の override が存在する | override の `desired` |
| 3 | `mode: pinned` | グループの `desired` |
| 4 | `mode: schedule` | cron 評価の結果 |

cron 評価は、`start_cron` と `stop_cron` それぞれの直近の過去の発火時刻を比較し、後に発火した方に従う。同時刻の場合は stop とする。評価に用いるタイムゾーンはグループの `timezone` であり、未設定時は reconciler の `DEFAULT_TIMEZONE` である。tzdata はバイナリに埋め込む。

## reconcile ループ

呼び出し 1 回がテーブル全体の 1 回の Scan から始まり、全グループを処理する。グループ 1 つあたりの処理は次のとおりである。

```
desired = 望ましい状態の解決            # disabled なら検出せずスキップ
resources = 検出(セレクタ)             # tag:GetResources
for resource in resources:
    actual = Describe()
    if actual が遷移中:
        skip                            # 次サイクルで再試行
    elif desired != actual:
        Stop() / Start()
        status# の更新 + 通知
```

ループの起点となる呼び出しには、定期実行、RDS 自動起動イベント、手動呼び出しの 3 経路がある。いずれもペイロードによって処理範囲が変わらない。詳細は、[trigger.md](trigger.md) の呼び出し経路を参照。

### 規則

ループが従う規則と、その根拠を、以下にまとめる。

| 規則 | 根拠 |
|---|---|
| 収束サイクルでは、書き込みも通知も行わない | — |
| エラーはリソース単位に隔離し、`status#` の `last_error` に記録して通知する | 1 件の失敗を他のリソース・グループへ波及させない |
| 検出直後の not-found はエラーではなくスキップとする | Tagging API は結果整合であり、タグ変更の反映に遅延がある |
| 遷移中のリソースはスキップし、遷移を初めて観測したサイクルで `transitioning_since` を 1 回だけ書く | スキップはエラーにも通知にもならず、これがないと終わらない遷移を収束済みと区別できない |
| 複数グループのセレクタが同一リソースに一致した場合、グループ名昇順で先のグループが管理する | 所有者を一意に決める |
| 無視される側のグループは、そのサイクル分の重複を 1 件にまとめて自身の `status#group#<名前>` に記録する | 所有グループが書くリソース側の `status#` と、記録先を分離する |
| グループレベルの障害(不正な cron・timezone、検出の失敗、セレクタの重複)は `status#group#<名前>` に記録して通知し、クリアはそのグループの処理をすべて終えたあとに 1 回だけ行う | 記録とクリアが同一サイクル内で交互に起きる通知の発振を避ける |
| リソース単位の失敗が 1 件以上あったサイクルでは、ループを完走したうえでハンドラがエラーを返す | 失敗を握りつぶすかどうかと、その結果を呼び出し側へ報告するかどうかは別に扱う |

### リソース種別ごとの操作

種別ごとに呼ぶ API を、以下に示す。

| 種別 | 停止 | 起動 |
|---|---|---|
| `rds-instance` / `rds-cluster` | `StopDBInstance` / `StopDBCluster` | `StartDBInstance` / `StartDBCluster` |
| `ec2-instance` | `StopInstances` | `StartInstances` |
| `ecs-service` | Application Auto Scaling のターゲットがあれば min/max を 0/0 に変更したのち、`UpdateService(desiredCount=0)` | `UpdateService` で desiredCount を復元したのち、min/max を復元 |

ECS では、起動時の desiredCount と Auto Scaling の min/max をリソース自身のタグから読む。停止時に元の値を保存して復元する形式ではない。Auto Scaling のターゲットの有無は `DescribeScalableTargets` で判定する。

ECS の停止は 2 段階であり、原子的ではない。min/max を 0/0 にしたあとで `UpdateService` が失敗した場合は、元の min/max へ巻き戻す。巻き戻さない場合、サービスが起動したままスケールアウト不能な状態で残るためである。

## state テーブルの診断

state テーブルの不整合(孤立レコード、セレクタの重複、終わらない遷移)は reconcile ループでは扱わず、`internal/app/doctor` が診断する。CLI と Web コンソールが同じ実装を共有する。

判定はテーブル全体の Scan と全グループの検出の両方に基づく。孤立しているかどうかが、どのグループのセレクタにも一致しないという事実でしか決まらないためである。したがって、検出が 1 つでも失敗したサイクルでは孤立の判定そのものを見送る。一時的に検出できなかっただけのリソースの監査記録を削除しないためである。

削除できるのは、対象が存在しないことが Scan と検出だけで確定するレコードに限る。設定そのもの、人間の判断を要する項目、および AWS リソースには触れない。

reconciler が孤立した `status#` を削除しないのも同じ理由による。検出結果は Tagging API の遅れによりずれるため、reconcile の最中の一致状況を根拠に監査記録を削除できない。

## リポジトリ構成

`internal/` は層ごとにディレクトリを分ける。依存は必ず内向き(下の表で上から下へ)であり、逆向きの import は存在しない。層と、その層が import してよい対象を、以下に示す。

| 層 | パッケージ | import してよいもの |
| --- | --- | --- |
| ドメイン | `core/model`、`core/schedule` | なし(標準ライブラリのみ) |
| 永続化 | `state` | `core` |
| アプリケーション | `app/groups`、`app/reconcile`、`app/doctor`、`app/port` | `core`、`state`、`app/port` |
| AWS アダプタ | `aws/tagging`、`aws/compute`、`aws/sns`、`aws/cloudwatch` | `core` |
| フロントエンド | `ui/cli`、`ui/webconsole` | `core`、`state`、`app`、`wire` |
| 合成 | `wire` | すべて |

```
.
├── cmd/                      # 薄い main のみ。処理は internal/ui と internal/wire にある
│   ├── reconciler/           # Lambda エントリポイント(bootstrap)
│   ├── cheapskate-cli/       # 設定 CLI
│   ├── webconsole/           # Web コンソール(Lambda / ローカル両対応)
│   └── dev-bootstrap/        # ローカル開発用の state テーブル作成(イメージには含まれない)
├── internal/
│   ├── core/
│   │   ├── model/            # ドメイン型と検証
│   │   └── schedule/         # 望ましい状態の解決
│   ├── state/                # DynamoDB state テーブル: アクセス層 + キー配置 + アイテム形式 + スキーマ定義
│   ├── app/
│   │   ├── port/             # アプリ層の外向きインターフェース(aws/ が実装する)
│   │   ├── groups/           # グループ設定操作(CLI と Web コンソールで共用)
│   │   ├── reconcile/        # reconcile ループ
│   │   └── doctor/           # state テーブルの診断と孤立レコードの削除(CLI と Web コンソールで共用)
│   ├── aws/
│   │   ├── tagging/          # セレクタ → リソースの検出(Tagging API を呼ぶ唯一のパッケージ)
│   │   ├── compute/          # rds / ecs / ec2 の describe/stop/start 実装
│   │   ├── sns/              # 通知の publish
│   │   └── cloudwatch/       # EMF メトリクスのログ出力(API 呼び出しはしない)
│   ├── ui/
│   │   ├── cli/              # cheapskate-cli の実装
│   │   └── webconsole/       # Web コンソールの HTTP ハンドラとテンプレート
│   ├── wire/                 # 合成ルート: aws アダプタを app/port に結びつける
│   └── devtools/             # 開発・テスト専用(Lambda イメージには含まれない)
│       ├── devseed/          # ローカル開発用のダミー ECS リソース
│       └── emutest/          # エミュレータ接続ヘルパ
├── tests/                    # 対象が単一パッケージではないテスト
│   ├── system/               # 実アダプタを結線した reconcile 一連動作(`integration` タグ)
│   └── image/                # ビルドしたコンテナイメージの外形検証(`image` タグ)
├── Dockerfile                # マルチステージビルド(クロスコンパイル対応)
├── compose.yaml              # ローカル AWS エミュレータ
└── docs/en, docs/ja/         # ドキュメント
```

### アプリ層から AWS アダプタへの非依存

アプリ層が外の世界に求めるものは、`app/port` の 4 つのインターフェース(`Discoverer` / `Target` / `Describer` / `Notifier`)として宣言される。`internal/aws` の各パッケージがそれを実装し、`internal/wire` が両者を結びつける。どの AWS クライアントがどのポートを満たすかを知っているのは `wire` だけである。

### state テーブルの位置づけ

state テーブルは差し替え可能な依存ではなく cheapskate 固有の構造であるため、ポートとしない。アプリ層は `internal/state` を直接使い、テストはその下の DynamoDB クライアントをモックする。

state への窓口は、利用側が必要分だけを宣言したインターフェースである。`reconcile.Store` にはグループ設定と override を書くメソッドが無く、`groups.Store` と `doctor.Store` には status を書くメソッドが無い。呼べないメソッドは存在しないため、読み書きの分離を規律で守る必要がない。

キー配置とアイテム形式も `internal/state` に閉じている。`core/model` はドメイン型だけを持ち、`pk` の組み立て方も `dynamodbav` タグも知らない。アプリ層はキー文字列を組み立てず、意図で名付けたメソッドを呼ぶ。

### ドメイン語彙の名前付き型

`ResourceType` / `Mode` / `DesiredState` / `ObservedState` / `Action` は、いずれも下地が string であるが別の型である。とくに `DesiredState` と `ObservedState` は同じ文字列を共有するため、型が別であることのみが取り違えを防いでいる。

### リソース種別の宣言の集約

種別ごとに異なる、振る舞いを伴わない事実は `model.TypeInfo` として `core/model/resource_<種別>.go` にあり、`resource.go` がそれを並べた登録簿となる。宣言する事実は次の 4 つである。

- ARN の形
- Tagging API のフィルタ
- `ref` の文法
- 設定として意味を持つタグ

describe/stop/start という振る舞いはここに置かない。AWS SDK のクライアントを要するため `port.Target` として外側にある。種別固有の値の扱いも、それを必要とするアダプタ側に置く。

### 設定遷移の実装位置

`pin` / `unpin` / `schedule` / `disable` / `set-selector` の規則は `model.GroupSpec` のメソッドにあり、`disable` を除くすべてが結果を検証してから返す。したがってアプリ層は、保存はできるが reconciler が従えない設定を書けない。

`disable` だけが検証しないのは、壊れた設定のグループを止めるための最後の手段であり、その壊れた設定を理由に失敗してはならないためである。

### モックの配置

生成モックは、インターフェースを宣言するパッケージの隣の `mocks/` に置く。

## 対応リソース種別の追加

変更するのは 3 か所であり、それぞれ層が異なる。reconcile ループ、state、望ましい状態の解決、診断、通知は種別を知らないため、変更を要しない。変更先と内容を、以下に示す。

| 変更先 | 内容 |
|---|---|
| `internal/core/model/resource_<種別>.go` を追加し、`resource.go` の `typeInfos` へ 1 行足す | 種別の定数、ARN の `service` と resource-type、`ref` が満たすべき正規表現、タグで与える設定項目を宣言する。`KnownTypes`、セレクタの検証、検出フィルタ、フロントエンドの列挙と設定表示は、すべてここから導かれる |
| `internal/aws/compute` に `port.Target` の実装を追加する | `Type()` / `Describe` / `Stop` / `Start` を実装し、使う AWS クライアントの部分集合をインターフェースとして宣言して `compute.go` の `//go:generate` 行に足す。`Start` は `model.Resource` を丸ごと受け取るため、種別固有の起動設定はそのタグから読める |
| `internal/wire` の `Targets` に 1 行足す | どの AWS クライアントがどのターゲットを裏打ちするかを知っているのは合成ルートだけである |

宣言と結線の食い違いは `internal/wire` のテストが検出する。宣言そのものの破れ(`RefPattern` の欠落、ARN の組の重複など)は `TestTypeInfoDeclarations` が検出する。

コードの外では、IAM ポリシーの記述に新しい種別の Describe と制御系 API を追加する。

## コンテナイメージ

reconciler と Web コンソールは別々のイメージとする。どちらもベースは `public.ecr.aws/lambda/provided:al2023` であり、静的バイナリ 1 つを `/var/runtime/bootstrap` として同梱する。同一 `Dockerfile` の `--target` で作り分け、Go のビルドステージを共有する。

分けているのは、両者のライフサイクルが独立しているためである。Web コンソールはオプトインであり、デプロイしない場合は push するイメージも 1 つで済む。コンソール側の変更で reconciler のイメージ digest が動かないため、必須コンポーネントに不要な再デプロイが波及しない。

両イメージとも Lambda 上で動作するが、ランタイムとの接続経路は異なる。reconciler は Go のハンドラを直接登録し、Web コンソールは同梱の Lambda Web Adapter を経由する。詳細は、[on_lambda.md](on_lambda.md) の組み込み状況の一覧を参照。

`cheapskate-cli` はイメージ化しない。Lambda 上で動かさないためであり、配布は各 OS 向けのアーカイブで行う。

## 依存

モジュール名は `cheapskate` である。ライブラリとしての import は想定しない。依存は AWS SDK v2、aws-lambda-go(reconciler のみ)、`adhocore/gronx`(cron 評価)、テスト用の testify と go.uber.org/mock である。Web コンソールは Lambda のライブラリを一切リンクしない。
