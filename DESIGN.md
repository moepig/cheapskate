# DESIGN: cheapskate — 実装方針

方式選定の経緯とコスト比較は [consider.md](consider.md) を参照。本書は採用した方式A(望ましい状態 + Reconcile ループ)の実装方針を定める。

## 要件

設計検討時の要件に加え、以下を満たす。

1. **Go で実装し、Lambda へはコンテナイメージでデプロイする**
2. **イメージはユーザーが各自のアカウントの ECR にセルフプッシュして使用する**。公開レジストリや配布基盤(SAR 等)に依存しない
3. **設定の CRUD を行う CLI ツール(`csctl`)を提供する**。インタフェースは CRUD の形にとらわれず、実態の操作としては DynamoDB のアイテムを扱う
4. **ユーザーの既存 IaC にシームレスに統合でき、独立した IaC コード群を要求しない**。CDK を使っている人に Terraform を要求しない、既に Terraform コーディング規則がある組織に別の Terraform 規則(独自モジュール等)を持ち込まない、といったことを含む
5. **必要な IAM ロールとポリシーを明確に定義する**
6. **ユーザーの命名規則に対応できる**(作成リソースの名前を制御可能に)
7. **IAM ロール等の周辺リソースを外部から与えられる**(BYO: bring your own)
8. **権限絞り込みの対象リソースを、任意個数のタグ(キー・値)とリソースタイプで指定できる**(タグの個数に上限なし、値はキーごとに複数指定可)
9. **IaC テンプレートは配布せず、全リソースを自前の IaC(または CLI・コンソール)で構築するための参照ドキュメントを成果物として提供する**

> 旧設計から 2 点を撤回した。(1)「AWS Serverless Application Repository (SAR) で公開する」— SAR はコンテナイメージパッケージの Lambda を配布できないため、配布は「ソース + Dockerfile を各自ビルドして ECR にセルフプッシュ」に一本化した。(2)「CloudFormation テンプレート(`template.yaml`)を配布する」— IaC テンプレートは一切配布せず、参照ドキュメント(`docs/*/usage/setup.md`)を契約として全リソースをユーザー自身が構築する方式に一本化した。これにより、旧テンプレートのパラメータで担っていた命名規則対応・BYO・Permissions Boundary は「全リソースがユーザー定義」であること自体で満たされ(要件 6・7)、権限絞り込み(要件 8)もテンプレートの表現力(固定スロット等)に縛られなくなった。

## 全体構成

```
                          ┌──────────────────────────┐
  EventBridge Scheduler   │      DynamoDB            │
  rate(N minutes) ────┐   │  state テーブル           │◄── csctl (CLI) / Terraform /
                      ▼   │ (config/override/status)  │    aws dynamodb put-item
  EventBridge Rule ─► Reconciler Lambda ◄─────────────┘
  (aws.rds イベント)      │  (Go / コンテナイメージ)
                          ├─ rds: Describe → Stop/Start
                          ├─ ecs: DescribeServices → UpdateService
                          │        (+ Application Auto Scaling min/max)
                          └─ SNS: アクション実行/失敗を通知
```

成果物(配布物)は 4 つ:

| 成果物 | 形態 | 用途 |
|---|---|---|
| Reconciler | ソース + `Dockerfile` | ユーザーがビルドし自アカウントの ECR へプッシュ |
| `csctl` | ソース(`go build ./cmd/csctl`)/ リリースバイナリ | 設定アイテムの操作 |
| Web コンソール | 同一コンテナイメージ内の別エントリポイント(オプトイン) | ブラウザからの設定アイテム操作(IP 制限のみ) |
| `docs/*/usage/setup.md` | 参照ドキュメント(構築の契約) | 全リソースを自前の IaC・CLI・コンソールで構築するための完全な仕様 |

## リポジトリ構成

```
.
├── cmd/
│   ├── reconciler/          # Lambda エントリポイント(bootstrap)
│   ├── csctl/               # 設定 CLI
│   └── webconsole/          # オプトインの Web コンソール(Lambda / ローカル両対応)
├── internal/
│   ├── model/               # データモデルと検証(config/override/status/observation)
│   ├── schedule/            # desired 解決(override > pinned > cron)
│   ├── store/               # DynamoDB アクセス層(Reconciler と CLI で共用)
│   ├── ops/                 # 設定操作の共通ロジック(csctl と webconsole で共用)
│   ├── webconsole/          # Web コンソールの HTTP ハンドラとテンプレート
│   ├── target/              # Target インターフェース + rds/ecs 実装
│   ├── reconcile/           # リコンサイルループ本体 + SNS Notifier
│   ├── dynafake/            # 単体テスト用インメモリ DynamoDB フェイク
│   └── emutest/             # 統合テスト用エミュレータ接続ヘルパ
├── Dockerfile               # クロスコンパイル対応のマルチステージビルド
├── compose.yaml             # ローカル AWS エミュレータ(Floci)
├── docs/en, docs/ja/        # ドキュメント(英語 / 日本語。usage/setup.md が構築の契約)
├── Makefile                 # build / test / lint / image / push
├── README.md / LICENSE / consider.md / DESIGN.md
```

- モジュール名はローカル完結の `cheapskate`(ライブラリとして import される想定はない)
- 依存は AWS SDK v2、aws-lambda-go、cron 評価の `adhocore/gronx`、Web コンソール用の `akrylysov/algnhsa`(API Gateway プロキシイベント → `http.Handler` の薄いアダプタ)のみ(gronx は「直近の過去の発火時刻」`PrevTickBefore` を提供し、croniter の `get_prev` に相当)
- タイムゾーンは `time/tzdata` を埋め込み、イメージ側の tzdata に依存しない

## DynamoDB 設計

テーブルは 1 つ、PK `pk` のみのシンプルキー。**config(ユーザー管理)と status(Reconciler 管理)を別アイテムに分離する**。Terraform で config アイテムを管理した場合に、Reconciler の書き込みが Terraform のドリフト検知に引っかからないようにするため。

```
pk = "config#<resource_id>"     ← ユーザー(csctl / Terraform)が書く。Reconciler は読むだけ
----------------------------------------------------------------
type          : rds-instance | rds-cluster | ecs-service
mode          : pinned | schedule | disabled
desired       : stopped | running          (mode=pinned のとき有効)
start_cron    : "0 9 * * MON-FRI"          (mode=schedule のとき)
stop_cron     : "0 20 * * MON-FRI"
timezone      : "Asia/Tokyo"
restore_count : 2                          (ecs-service のみ。省略時は status の記録値)

pk = "override#<resource_id>"   ← 人が一時的に書く(csctl override)。TTL 属性で自動消滅
----------------------------------------------------------------
desired       : running | stopped
expires_at    : epoch 秒(DynamoDB TTL。TTL 削除は遅延するためコード側でも期限を強制)

pk = "status#<resource_id>"     ← Reconciler だけが書く(監査・復元用)
----------------------------------------------------------------
observed_state / last_action / last_action_at / last_error / last_error_at /
saved_desired_count / saved_scaling_min / saved_scaling_max
```

- **status# の各フィールドは last_action_at / last_error_at 時点のスナップショットであり、現在のライブ状態ではない(B-10)**。収束サイクル(何もアクションしない周期)では書き込みを行わない設計(全リソースを 5 分ごとに毎回書き込むコストを避けるため)なので、cheapskate の外側でリソースが変更されると `observed_state` は古いまま残り得る。csctl list / show のヘッダは `OBSERVED(AT LAST ACTION)` と明記し、webconsole も同様に表示する

- `resource_id` 例: `rds-cluster#my-aurora`、`rds-instance#my-db`、`ecs#my-cluster/my-service`。型プレフィックスから `type` が導出できるため、CLI では型指定が不要
- desired の決定順序: **mode=disabled は override より強く常にスキップ**。disabled でなければ **override(期限内)> mode=pinned の desired > cron 評価**。`csctl override` / webconsole は disabled な config への override 登録を拒否する(スケジュール/pin してから override すること)

## Reconciler Lambda の処理フロー

1 本の Lambda がペイロードでディスパッチする。

- **定期起動(Scheduler)**: `source` フィールドを持たない(または `aws.rds` 以外の)ペイロード — 通常は `{}` — を受けたら全 config アイテムを Scan → 全リソースをリコンサイル
- **RDS イベント(EventBridge Rule)**: `event.source == "aws.rds"` を受けたら、イベント中の識別子から `resource_id` を組み立て、該当 1 件だけをリコンサイル(高速パス)。未登録リソースのイベントは無視
- **未知の source(C-4)**: `source` が空でも `aws.rds` でもない場合、フルリコンサイルにフォールバックしつつ `unexpected-event-source` を警告ログに出す。将来 EventBridge 側のトリガーが増えても無言で全件走査に落ちないようにするための保険であり、`{}` によるフル起動は正規の呼び出し方として維持する

1 リソースあたりの処理:

```
desired = resolve_desired(config, override, now)   # cron は「直近どちらのイベントが後か」で判定(同時刻は stop)
actual  = target.Describe()                        # RDS: DBInstanceStatus / ECS: desiredCount
if actual が遷移中(starting/stopping 等):
    skip                                           # 次周期で再試行(待ち合わせ処理を持たない)
elif desired != actual:
    target.Stop() / target.Start()
    status アイテム更新 + SNS 通知
```

実装上の規約:

- per-resource でエラーを閉じ込め、1 件の失敗が他を巻き込まない。失敗は status に記録し SNS 通知
- 毎周期の「差分なし」では書き込みも通知もしない(DynamoDB 書き込みとノイズ通知の抑制)
- 関数の reserved concurrency = 1。定期起動とイベント起動の重複実行を排除する
- Describe が空リストを返した場合も not-found として扱う(NotFound 例外と挙動が揺れる実装・エミュレータへの防御)
- ログは slog の JSON 構造化(resource_id / desired / observed / action)

### RDS イベントの高速パス

EventBridge ルールのイベントパターン:

```yaml
source: [aws.rds]
detail-type: ["RDS DB Instance Event", "RDS DB Cluster Event"]
detail:
  EventID:
    - RDS-EVENT-0154   # インスタンス: 停止期限(7日)超過で自動起動
    - RDS-EVENT-0153   # クラスター: 同上
    - RDS-EVENT-0088   # インスタンス起動完了
    - RDS-EVENT-0151   # クラスター起動完了
```

0153/0154 受信時点では `starting` で Stop できないため実際の停止は起動完了イベント(0088/0151)側の周回で実行される。イベントを取りこぼしても定期リコンサイルが拾う。

### ECS の扱い

- 停止 = `UpdateService(desiredCount=0)`。実行前の desiredCount を status に保存し、復帰時は `restore_count`(config 指定があればそちら)へ戻す。どちらも無ければ 1
- Application Auto Scaling が付いている場合(`DescribeScalableTargets` で判定)、desiredCount 変更はポリシーに巻き戻されるため `RegisterScalableTarget` で MinCapacity/MaxCapacity を保存 → 0/0 に変更、復帰時に復元する

## CLI(csctl)

設定操作の実態は DynamoDB のアイテム CRUD だが、インタフェースは**意図ベースの動詞**にする(「config アイテムを PUT する」ではなく「この DB を止めたままにする」)。

```
csctl list                                        # 登録リソース一覧(override/status を結合表示)
csctl show <resource-id>                          # config + override + status を JSON で
csctl pin <resource-id> running|stopped           # 固定(旧 cron 設定は保持され schedule で復帰可)
csctl schedule <resource-id> -start CRON -stop CRON [-timezone TZ] [-restore-count N]
csctl disable <resource-id>                       # 設定を残したまま管理停止
csctl override <resource-id> running|stopped -for 2h   # 期限付き上書き(TTL で自動消滅)
csctl override <resource-id> -clear
csctl remove <resource-id>                        # config/override/status を削除
```

設計方針:

- **DynamoDB 以外の AWS API は呼ばない**。RDS/ECS を直接操作するのは Reconciler だけ、という責務分離を CLI でも守る(CLI に必要な IAM 権限もテーブルのみに閉じる)
- テーブル名は `-table` フラグまたは環境変数 `CHEAPSKATE_TABLE`。認証・リージョン・エンドポイントは AWS SDK の標準チェーン(`AWS_ENDPOINT_URL` が効くのでローカルエミュレータにもそのまま繋がる)
- 書き込み前に検証する: resource_id の形式(型プレフィックス)、desired の値、cron 式(gronx)、タイムゾーン、`-restore-count` の型整合(ecs のみ)。未登録リソースへの override は無効なので拒否する
- config アイテムを Terraform 管理にしている場合、`csctl pin/schedule/disable/remove` の書き込みは Terraform とドリフトする。恒久設定は IaC・一時操作は `csctl override`、という使い分けを README で案内する(override# は TTL 付きで IaC 管理しない前提)
- store 層は Reconciler と共用。CLI 固有のデータアクセスコードを持たない
- 操作ロジック(検証 + store 呼び出し)は `internal/ops` に置き、Web コンソールと共用する

## Web コンソール(オプトイン)

必要な場合だけユーザーが API Gateway + Lambda を追加構築するオプトインのブラウザフロントエンド。csctl と同じ操作を `internal/ops` 経由で提供する。

- 技術スタック: Go `net/http` + `html/template`(`embed` でバイナリに同梱)。JavaScript・外部アセット・フロントエンドビルドチェーンなし。Lambda 上では algnhsa で API Gateway プロキシイベントを `http.Handler` に変換し、ローカルでは同じハンドラを `go run ./cmd/webconsole` でそのまま listen する
- ホスティング: API Gateway **REST API(v1)** + 同一コンテナイメージの Lambda(`ImageConfig.EntryPoint` で `/var/runtime/webconsole` に切替)。REST API を選ぶ理由はリソースポリシー(HTTP API には無い)による IP 制限
- アクセス制御: リソースポリシーの `NotIpAddress` Deny による IP 許可リストのみで、認証は持たない。許可 CIDR 内の人は誰でも操作できる、という割り切りをドキュメントに明記する
- 責務分離は csctl と同じ: 実行ロールは state テーブルの Scan/Get/Put/Delete のみで、RDS/ECS API は呼ばない
- CSRF: セッションが無いため古典的なトークンは不要だが、外部ページからの誘導 POST を防ぐため `Origin` / `Sec-Fetch-Site` を検証する

## デプロイ方式(コンテナ / ECR セルフプッシュ)

- `Dockerfile` はマルチステージ。ビルドステージは `--platform=$BUILDPLATFORM` でホストネイティブ実行し `GOARCH` でクロスコンパイルするため、x86 ホストから arm64 イメージを QEMU なしでビルドできる。実行ステージは `public.ecr.aws/lambda/provided:al2023` に静的バイナリを `/var/runtime/bootstrap` として置くだけ(イメージ約 45MB)
- 既定アーキテクチャは arm64(最安)。テンプレートの `Architecture` パラメータとイメージのプラットフォームを一致させる
- プッシュ先はユーザー自身の ECR リポジトリ: `make push ECR_REPO=<uri>`(中身は `aws ecr get-login-password | docker login` → `docker tag` → `docker push`)
- 同一アカウントの ECR からの Lambda イメージ取得に追加のリポジトリポリシーは不要。デプロイ操作者に `ecr:BatchGetImage` / `ecr:GetDownloadUrlForLayer` があればよい
- ベースイメージ同梱の Runtime Interface Emulator により、`docker run` + HTTP POST でローカル起動確認ができる(テスト戦略参照)

## 構築方式(IaC テンプレートは配布しない)

CloudFormation テンプレート・Terraform モジュール・CDK Construct は配布しない。`docs/*/usage/setup.md` を「作成するリソースの完全な契約」として提供し、ユーザーは自分のツール(Terraform / CDK / CloudFormation / コンソール / CLI)と自分の規則(命名・タグ付与・Permissions Boundary)で全リソースを構築する。実行例は任意の IaC に 1:1 で読み替えられる AWS CLI で示す。

構築するリソース(詳細・実行例は setup.md):

| リソース | 要点 |
|---|---|
| DynamoDB state テーブル | PK `pk`(S)のみ、TTL 属性 `expires_at` 有効。課金モードは任意 |
| Reconciler Lambda | セルフプッシュしたコンテナイメージ。reserved concurrency = 1 |
| Lambda 実行ロール | 「IAM ロールとポリシー」節が契約 |
| EventBridge Scheduler | rate(N 分)で Lambda を起動。実行ロールは `lambda:InvokeFunction` のみ |
| EventBridge Rule | RDS 自動起動/起動完了イベント。Lambda リソースポリシーで起動許可 |
| SNS トピック(オプション) | 省略時は通知無効 |
| Web コンソール(オプトイン) | API Gateway REST API + 同一イメージ別エントリポイントの Lambda |

- 設定は Lambda 環境変数(`STATE_TABLE_NAME` / `DEFAULT_TIMEZONE` / `NOTIFICATION_TOPIC_ARN`、Web コンソールは加えて `BASE_PATH`)と各リソース定義の属性(リコンサイル間隔、ログ保持期間、アーキテクチャ等)で表現し、cheapskate 側にパラメータ機構を持たない
- 旧テンプレートがパラメータで担っていた命名規則対応(`ResourceNamePrefix`)、周辺リソースの外部注入(`Existing*`)、`PermissionsBoundaryArn` は、全リソースがユーザー定義になったことで概念ごと不要になった

## 既存 IaC への統合(ユーザー視点)

方針: **本プロジェクトが配布するのはソースとドキュメントだけ**。テンプレート・モジュール・Construct を「正」として配布せず、ユーザーは setup.md の契約を自分のツール・自分のコーディング規則の中で書ける。

- **Terraform**: 全リソースを自前の `.tf` で記述。リソース登録は `aws_dynamodb_table_item`(Reconciler は config に書き込まないためドリフト検知と競合しない)
- **CDK**: `DockerImageFunction`(`fromImageAsset` なら CDK が ECR プッシュまで行う=セルフプッシュの CDK ネイティブ形)+ 標準 L1/L2 リソース
- **CloudFormation**: 契約に沿って自前のテンプレートを書く(本プロジェクトからの配布はない)
- **IaC なし**: setup.md の AWS CLI 実行例をそのまま実行、登録は `csctl`

## IAM ロールとポリシー

必要なロールは 2 つ。いずれもユーザーの IaC で作成するもので、以下がその契約(満たすべき信頼ポリシーと権限)。命名・タグ・Permissions Boundary は組織の規則で自由に付けてよい。

### 1. ReconcilerRole(Lambda 実行ロール)

信頼ポリシー: `lambda.amazonaws.com`

| Sid | Action | Resource | 備考 |
|---|---|---|---|
| Logs | logs:CreateLogGroup / CreateLogStream / PutLogEvents | 当該関数のロググループ | |
| State | dynamodb:Scan / GetItem / PutItem / UpdateItem | StateTable の ARN | テーブル 1 つに限定 |
| RdsRead | rds:DescribeDBInstances / DescribeDBClusters | `*` | Describe 系はリソースレベル制限不可 |
| EcsRead | ecs:DescribeServices | `*` | |
| Autoscaling | application-autoscaling:DescribeScalableTargets / RegisterScalableTarget | `*` | リソースレベル制限非対応 |
| Write | rds:StopDBInstance / StartDBInstance / StopDBCluster / StartDBCluster / ecs:UpdateService | `*`(絞り込みオプションあり) | タグ条件・タイプ別分割は下記 |
| Notify | sns:Publish | NotificationTopic の ARN | |

**絞り込みオプション**(いずれも上記ポリシーの書き方のバリエーションであり、Reconciler の挙動は変わらない。setup.md に記載してユーザーのポリシー定義に委ねる):

- **リソースタイプ**: Write をタイプごとのステートメント(RdsInstanceWrite / RdsClusterWrite / EcsWrite)に分割し、管理しないタイプの文を省く。あわせて ECS を使わないなら EcsRead / Autoscaling 文、RDS を使わないなら RdsRead 文も省ける。権限外タイプの config を登録した場合は実行時の AccessDenied として status / SNS に現れる
- **タグ**: Write 文に `aws:ResourceTag/<キー>` の `StringEquals` 条件を付け、管理対象の RDS/ECS リソースにそのタグを付ける。値は配列で複数指定できる(OR)。複数タグの**いずれか**で許可する(OR)にはタグごとに文を複製し、**すべて**を要求する(AND)には同一 Condition ブロックに複数キーを書く。自前ポリシーなのでタグの個数・組み合わせに上限はない

既存の社内タグ体系をそのまま流用でき、「DynamoDB 登録 + タグ」の二重オプトインになる。

このロールが**やらないこと**: RDS/ECS の作成・削除・変更(Stop/Start/desiredCount 以外)、KMS 操作、他テーブルへのアクセス。

### 2. SchedulerRole(EventBridge Scheduler 実行ロール)

- 信頼ポリシー: `scheduler.amazonaws.com`(`aws:SourceAccount` 条件付き — confused deputy 対策)
- 権限: `lambda:InvokeFunction` を ReconcilerFunction の ARN に限定

### 3. EventBridge Rule → Lambda(ロール不要)

ルールからの起動は **Lambda のリソースベースポリシー**(`AWS::Lambda::Permission`、principal `events.amazonaws.com`、SourceArn = ルール ARN)で許可する。

### 4. csctl 利用者

state テーブルに対する `dynamodb:Scan / GetItem / PutItem / DeleteItem` のみ。

## テスト戦略(ローカル)

方針: **ロジックは AWS 非依存の単体テストで網羅し、AWS SDK を使う層だけ Floci(ローカル AWS エミュレータ、LocalStack Community 互換)への結合テストで検証する**。実 AWS でしか起きない事象はローカルの対象外として境界を明記する。

| レイヤ | 対象 | 手段 |
|---|---|---|
| 単体(go test) | desired 解決(cron/override/タイムゾーン/優先順位)、config 検証、RDS イベント → resource_id 解決、収束判定、ECS の保存/復元 | 時刻は引数注入。AWS クライアントは narrow interface + フェイク(DynamoDB は `internal/dynafake`) |
| 結合(go test -tags integration) | store の実 DynamoDB 呼び出し、リコンサイル一連動作(Scan → Describe → Stop/Start → status 書込 → SNS 実配信)、csctl の各コマンド | Floci(`make floci-up`)。SNS 配信は SQS 購読プローブで実受信を検証 |
| コンテナスモーク | イメージが Lambda ランタイムとして起動・応答すること | ベースイメージ同梱の Runtime Interface Emulator に HTTP POST、Floci に接続 |
| 受け入れ(実 AWS) | 7 日自動起動、実際の遷移タイミング、RDS Stop/Start API、Auto Scaling の巻き戻り実挙動 | dev アカウントへデプロイ:(1) pinned stopped の RDS を手動起動 → 次周期で停止、(2) schedule の ECS が時刻で 0/復元 |

### Floci の使い方と境界

- 接続は環境変数 `AWS_ENDPOINT_URL`(SDK が標準で解釈)。**プロダクションコードにエンドポイント分岐やテスト用フックを持ち込まない**
- 手動確認・CI 共通で `compose.yaml` のコンテナを使う。エミュレータ不在時、統合テストは fail ではなく skip する
- **Floci は `StopDBInstance` / `StartDBInstance`(cluster 同様)を未実装**。統合テストでは Describe だけ実クライアントを使い、Stop/Start を記録に置き換えたスパイターゲットで代替する。当該 API の実呼び出しは受け入れ試験でカバーする
- 遷移中(starting/stopping)のスキップ動作はエミュレータで再現しづらいため、遷移中状態を返すフェイクを注入した単体テストで担保
- 採用理由: LocalStack Community が認証トークン必須化・更新凍結(2026-03)となったため、MIT ライセンス・認証不要・drop-in 互換の Floci を採用

## 運用方針

- 可観測性: SNS 通知(アクション時・失敗時のみ)+ CloudWatch Logs(slog JSON)。失敗継続の検知には `last_error` 通知と Lambda Errors メトリクスのアラームを README で案内

## 決定事項サマリ

| 論点 | 決定 | 理由 |
|---|---|---|
| 実装言語 / ランタイム | Go / コンテナイメージ(provided.al2023 + 静的バイナリ、arm64 既定) | 要件。単一静的バイナリで依存最小、イメージ ~45MB |
| 配布 | ソース + Dockerfile を各自ビルドし ECR にセルフプッシュ | 要件。公開レジストリ・SAR に依存しない(SAR はコンテナ Lambda 非対応のため旧要件を撤回) |
| 構築方式 | IaC テンプレートは配布せず、参照ドキュメント(setup.md)を契約として自前構築 | テンプレートの保守・パラメータ化が不要。ユーザーの IaC 規則に完全に従える |
| 設定インタフェース | `csctl` CLI(意図ベースの動詞、実態は DynamoDB アイテム操作) | 要件。DynamoDB 以外の AWS API は呼ばず責務分離を維持 |
| IaC 向け成果物 | ドキュメント内の AWS CLI 実行例のみ(テンプレート/モジュール/Construct は配布しない) | 既存のコーディング規則に別規則を持ち込まないため |
| 恒久設定と一時操作 | 恒久 = IaC または csctl、一時 = csctl override(TTL) | Terraform 管理 config とのドリフトを役割分担で回避 |
| ローカルテスト | ロジックは単体(フェイク + 時刻注入)、SDK 層は Floci、コンテナは RIE スモーク | エミュレータ接続は `AWS_ENDPOINT_URL` のみでコード無改変。RDS Stop/Start 未実装はスパイで代替し受け入れ試験でカバー |
| cron 評価 | adhocore/gronx の `PrevTickBefore`(同時刻タイは stop) | croniter の get_prev 相当。フェイルセーフ(安い側)に倒す |
| Lambda 本数 | 1 本(ペイロードでディスパッチ) | 定期・イベントでロジックを共有、構成最小 |
| config と status の分離 | 別アイテム(`config#`/`override#`/`status#`) | Terraform 管理アイテムへの書き込みによるドリフトを回避 |
| 待ち合わせ処理 | 持たない(遷移中はスキップ) | 次周期の再試行で収束。Step Functions 不要([consider.md](consider.md)) |
| IAM ロール | ユーザーが作成(本書とドキュメントは契約のみ定義) | 命名・boundary・タグを組織規則にそのまま従わせられる |
| 権限絞り込み | タグ条件(任意個数、OR/AND とも表現可)+ リソースタイプ単位の付与を契約ドキュメントで規定 | 最小権限と既存タグ体系の流用。自前ポリシーなので個数・組み合わせに制約なし |
| 命名規則・BYO | 対応機構なし(全リソースがユーザー定義のため概念ごと不要) | テンプレートが無い以上、命名パラメータも外部注入も存在しない |
