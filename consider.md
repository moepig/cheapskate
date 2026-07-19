# RDS/Aurora・ECS 停止維持 & スケジュール起動停止の設計検討

作成日: 2026-07-18

## 要件

1. RDS/Aurora を 1 週間以上停止させ続けたい(AWS は停止後 7 日で強制自動起動するため、自動起動されたら即座に再停止したい)
2. 望ましい状態が「起動」のリソースには手を出さない
3. スケジュールベースの起動・停止も設定できるようにしたい
4. 同じ仕組みを ECS サービスの desiredCount にも適用したい
5. サーバーレスで実現したい
6. 望ましい状態(desired state)は DynamoDB に保持する

## 推奨アーキテクチャ(方式A): 望ましい状態 + Reconcile ループ

Kubernetes コントローラと同じ発想で、**「イベントで即応 + 定期リコンサイルで保証」の二段構え**とし、スケジュール機能は「望ましい状態の計算ルール」として同じループに統合する。

```
                          ┌──────────────────────────┐
  EventBridge Scheduler   │      DynamoDB            │
  rate(5 minutes) ────┐   │  resource-state テーブル  │
                      ▼   │ (desired state + schedule)│
  EventBridge Rule ─► Reconciler Lambda ◄─────────────┘
  (aws.rds イベント:      │
   自動起動検知で即発火)   ├─ rds: Describe → Stop/Start
                          ├─ ecs: DescribeServices → UpdateService
                          └─ SNS: 実行/失敗を通知
```

### 構成要素

| コンポーネント | 役割 |
|---|---|
| DynamoDB 1 テーブル | desired state・スケジュール定義・復元情報 |
| Reconciler Lambda 1 本 | 全アイテム走査 → Describe → 差分があれば Stop/Start/UpdateService |
| EventBridge Scheduler | rate(5 minutes) で Reconciler を起動 |
| EventBridge Rule | RDS-EVENT-0153/0154 等で同じ Lambda を即時起動(高速パス) |
| SNS(+ Slack 等) | アクション実行・失敗の通知 |

### 要件へのマッピング

**1. 停止維持(自動起動の即時停止)**

- 7 日超過の強制起動時には専用イベントが発火する
  - インスタンス: `RDS-EVENT-0154`(停止期限超過で起動)
  - Aurora クラスター: `RDS-EVENT-0153`
- EventBridge ルールで拾って Reconciler を即時起動する
- ただし自動起動直後は `starting` 状態で StopDBCluster が撃てない。イベント駆動だけで完結させると待ち合わせ(Step Functions の Wait 等)が必要になり複雑化する。**リコンサイルループなら「遷移中はスキップして次周期に再試行」で自然に解決する**(これがループ方式を推す最大の理由)。
- より速く止めたい場合は「DB cluster started」(起動完了時)イベントもルールに追加する。

**2. 起動ステータスなら触らない**

- Reconciler は毎回「DynamoDB の desired」と「Describe した実状態」を比較し、一致していれば何もしない。
- **DynamoDB の desired が唯一の正(source of truth)**。起動しておきたいときは desired を書き換える運用にする。

**3. スケジュールベースの起動・停止**

- スケジュール定義(cron 式)を DynamoDB のアイテム属性として持ち、Reconciler が毎周期「今あるべき状態」を計算する(下記の方式比較で案A)。
- スケジュールも「desired state を決める入力」に過ぎず、**AWS API を叩くのは Reconciler だけ**に一本化する。これにより「スケジュール起動直後に停止維持ロジックが止めてしまう」類の競合が構造的に発生しない。

**4. ECS サービス**

- 同じテーブル・同じループに載せる。`UpdateService` で desiredCount を 0 / 復元値に切り替える。
- 0 にする前の desiredCount をアイテムに保存し(`restore_count`)、復帰時に戻す。
- **注意**: Application Auto Scaling が付いているサービスは desiredCount を直接変更してもスケーリングポリシーに巻き戻されるため、`RegisterScalableTarget` で min/max を 0 にする方式に切り替える。

### DynamoDB スキーマ案

```
PK: resource_id   例: "rds-cluster#my-aurora", "ecs#my-cluster/my-service"
----------------------------------------------------------------
type          : rds-instance | rds-cluster | ecs-service
mode          : pinned | schedule | disabled
desired       : stopped | running          (mode=pinned のとき有効)
start_cron    : "0 9 * * MON-FRI"          (mode=schedule のとき)
stop_cron     : "0 20 * * MON-FRI"
timezone      : "Asia/Tokyo"
restore_count : 2                          (ECS のみ)
override_until: 2026-07-25T00:00:00+09:00  (期限付きで手動状態を尊重、任意)
last_action / last_action_at / last_error  (監査・通知用)
```

- `override_until`: 「メンテで 2 時間だけ起動したい」ときに期限付きで desired を上書きする用。期限切れ判定も Reconciler の周期処理に載る。

### 実装上の注意

- 1 リソースの失敗が他を巻き込まないよう per-item で try/catch する
- Stop/Start は状態を確認してから発行し、冪等にする(遷移中はスキップ → 次周期で再試行)
- 通知は「アクションを実行したとき」と「失敗したとき」のみ(毎周期は出さない)

## 方式比較

| | 方式A(推奨) | 方式B | 方式C(参考) |
|---|---|---|---|
| 概要 | 5 分周期の Reconcile ループ。スケジュールは DynamoDB 内の cron 式を Lambda が評価 | リソース毎に EventBridge Scheduler(起動/停止 2 本)+ イベント駆動ガード(Lambda + SQS 遅延リトライ) | Step Functions でループを構成(Map で全リソース処理) |
| 長所 | インフラが増えない。スケジュール変更は DynamoDB 書き込みだけ。停止維持とスケジュールが同一ループで競合しない。ドリフト(手動操作・イベント取りこぼし)を自動補正 | 時刻精度が正確。実行回数が最少 | ワークフローの可視化 |
| 短所 | 時刻精度は周期粒度(5 分) | スケジューラーリソースのライフサイクル管理(作成・削除の同期)が増える。**ドリフト検知がない**(補うには結局ループを足すことになり方式A と同額になる) | Standard は遷移課金でループと相性が最悪(下記コスト参照) |

起動・停止用途に秒単位の精度は不要なため、5 分粒度で足りる方式A で十分。

## コスト試算(2026-07 時点、東京リージョン、概算)

### 前提

- 30 日/月、無料枠は適用しない(ワーストケース)
- 管理対象 20 リソース(RDS/Aurora 10 + ECS 10)
- 単価: Lambda $0.20/100万リクエスト + $0.0000167/GB秒、EventBridge Scheduler $1.00/100万回(無料枠 月 1,400 万回・アカウント全体・恒久)、Step Functions Standard $0.025/1,000 遷移、DynamoDB オンデマンド 書込 ~$0.71/100万・読取 ~$0.14/100万
- AWS サービスイベント(RDS イベント)の EventBridge ルールマッチは**無料**

### 方式A: 5 分周期リコンサイル(Lambda 256MB・平均 5 秒/回)

| 項目 | 計算 | 月額 |
|---|---|---|
| EventBridge Scheduler | 8,640 回(12回/h × 720h) | $0.009(無料枠内なら $0) |
| Lambda リクエスト | 8,640 回 | $0.002 |
| Lambda 実行時間 | 8,640 × 5秒 × 0.25GB = 10,800 GB秒 | $0.18 |
| DynamoDB | 読取 ~43K RRU + 書込少量 | ~$0.02 |
| CloudWatch Logs | ~20MB 取り込み | ~$0.02 |
| **合計** | | **~$0.23** |

### 方式B: リソース毎 Scheduler + イベント駆動ガード

スケジュール 40 本(20 リソース × 起動/停止)、自動起動イベント 月 ~43 回(停止維持 10 台 × 30/7)、SQS 遅延リトライ 10 回/件と仮定。

| 項目 | 計算 | 月額 |
|---|---|---|
| Scheduler 起動 | 40本 × 2回/日 × 30日 = 2,400 回 | $0.002 |
| Lambda(スケジュール + ガード) | ~3,000 回 × 1秒 × 0.25GB | ~$0.01 |
| SQS | ~500 リクエスト | ~$0 |
| **合計** | | **~$0.02** |

### 方式C: Step Functions ループ(5 分周期、Map で 20 リソース、1 周 ~68 遷移)

| 項目 | 計算 | 月額 |
|---|---|---|
| SFN Standard 遷移 | 8,640 実行 × 68 遷移 = 58.8 万遷移 | **$14.7** |
| 内部の Lambda タスク | 17.3万回 × 1秒 × 0.25GB | ~$0.76 |
| **合計(Standard)** | | **~$15.5** |
| (Express に替えた場合) | 実行課金 + GB 秒課金 | ~$1〜2 |

### スケール感度

| シナリオ | 方式A | 方式C(SFN Standard) |
|---|---|---|
| 20台・5分周期 | ~$0.2 | ~$15 |
| 200台・1分周期 | ~$4(Lambda 512MB × 10秒/回) | **~$660**(43,200 実行 × ~608 遷移 = 2,630 万遷移) |

### コスト面の結論

- 方式A と方式B の差($0.2/月)は判断材料にならない。選定理由は運用のシンプルさで**方式A 推奨**。
- 唯一の落とし穴は **Step Functions Standard の Map ループ**(状態遷移ごと課金)。ワークフローとして使うなら Express にすれば Lambda 並みに戻る。
- この仕組みが生む削減額との比較: 例えば db.r6g.large 1 台の停止維持で月 ~$220〜230(≈$0.31/h)の削減。制御系コスト $0.2/月はノイズで、1 台 × 1 時間分の停止で元が取れる。
- 非サーバーレス比較: Fargate スケジュールタスク(0.25vCPU/0.5GB を 5 分毎 30 秒)で ~$1.1/月、t4g.nano 常駐で ~$3〜4/月。サーバーレス構成が最安。

## 補足

- AWS Solutions の **Instance Scheduler on AWS** はスケジュール起動・停止のみで「7 日超の停止維持」も ECS も対象外のため、今回の要件では自作が妥当(タグでスケジュールを指定する UX は参考になる)。
- Aurora Serverless v2 は min ACU 0 の自動 pause で要件を満たせる場合があるため、対象が Serverless v2 ならこの仕組みの対象外にできる。
- IaC は CDK / Terraform どちらでも素直に書けるボリューム(Lambda 1〜2 本 + テーブル + ルール数個)。

## 参考リンク

- [Amazon EventBridge pricing](https://aws.amazon.com/eventbridge/pricing/)
- [Amazon EventBridge Scheduler](https://aws.amazon.com/eventbridge/scheduler/)
- [AWS Step Functions Pricing](https://aws.amazon.com/step-functions/pricing/)
