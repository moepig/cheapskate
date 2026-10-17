# セットアップ

本ドキュメントは、DynamoDB と Lambda の準備から、最初のグループを登録して定期制御を有効にするまでの手順を示す。

## 導入前の準備

reconciler 用 Lambda、DynamoDB テーブル、5 分間隔の呼び出し、および管理対象への IAM 権限が必要である。操作には CLI または任意の Web コンソールを使用する。AWS リソースは、使用する IaC または AWS の管理ツールで作成する。

reconciler と Web コンソールは別々のコンテナイメージである。イメージのビルドと ECR への登録は、[ビルド](../development/build.md)を参照。必要な AWS リソースと権限は、[AWS リソースと IAM](aws_resources.md)を参照。

## 1. DynamoDB テーブルの作成

String 型のパーティションキー pk と String 型のソートキー sk を持つテーブルを作成する。TTL とセカンダリインデックスは設定しない。on-demand で作成する例を、以下に示す。

```console
aws dynamodb create-table \
  --table-name cheapskate \
  --billing-mode PAY_PER_REQUEST \
  --attribute-definitions AttributeName=pk,AttributeType=S AttributeName=sk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH AttributeName=sk,KeyType=RANGE
```

## 2. reconciler の配置

Lambda のロググループを事前作成し、実行ロールに管理対象と使用する機能に応じた権限を付与する。ECS の Auto Scaling を使用する場合は、scalable target とサービスリンクロールも先に準備する。ポリシー例と ARN の確認方法は、[AWS リソースと IAM](aws_resources.md)を参照。

reconciler イメージを Lambda に配置する。イメージと Lambda のアーキテクチャを一致させる。STATE_TABLE_NAME にテーブル名、DEFAULT_TIMEZONE にスケジュールのタイムゾーンを設定する。

想定する最大リソース数で全体の処理が完了する timeout を設定する。1 回の実行ですべてのグループとタグ付きリソースを処理し、分割実行や checkpoint は行わない。Lambda timeout の最大値は 900 秒である。

5 分間隔で Lambda を呼び出す EventBridge Scheduler または EventBridge rule を 1 個作成する。この段階では定期呼び出しを無効にし、確認後に有効にする。グループごとの呼び出し設定は不要であり、payload は任意の JSON でよい。

## 3. 管理インターフェースの準備

信頼できる環境に cheapskate-cli を配置し、AWS の認証情報とリージョンを設定する。CLI のテーブル名とタイムゾーンの設定例を、以下に示す。

```sh
export AWS_REGION=ap-northeast-1
export CHEAPSKATE_TABLE=cheapskate
export DEFAULT_TIMEZONE=Asia/Tokyo
```

CLI と reconciler では同じ DEFAULT_TIMEZONE を使用すること。Web コンソールを使用する場合も同じタイムゾーンにする。認証と公開ホストの設定は、[Web コンソール](web_console.md#公開とアクセス設定)を参照。その他の変数は、[実行時設定](config.md)を参照。

## 4. グループ設定の登録

グループ設定は CLI・Web コンソールまたは Terraform のどちらかで管理する。CLI で平日 09:00～18:00 のグループを登録する例を、以下に示す。

```console
cheapskate-cli schedule --group dev -start '0 9 * * 1-5' -stop '0 18 * * 1-5'
```

ECS を管理する場合は、service の起動台数と通常の上限を決め、タグ値以上の ecs_max_count をグループに登録すること。登録手順は、[ECS 台数上限の登録](operations.md#ecs-台数上限の登録)を参照。

Terraform でグループを管理する場合の例は、[Terraform によるグループ設定](terraform_records.md)を参照。同じグループを Terraform と CLI・Web コンソールの両方で変更してはいけない。

## 5. リソースタグの設定

管理対象の各リソースに cheapskate:group=dev を設定する。ECS では service 自身に起動台数、通常の上下限、および上下限管理モードを設定する。値の制約は、[リソースタグ](resource_tag.md)を参照。

## 6. 動作確認と定期実行の有効化

reconciler を 1 回手動で呼び出す。設定登録時刻がスケジュールの稼働時間帯なら起動、停止時間帯なら停止が要求される。確認用コマンドを、以下に示す。

```console
cheapskate-cli -output json show --group dev
```

ログの summary と各リソースの現在状態を確認し、エラーがない状態で 5 分間隔の呼び出しを有効にする。状態確認は、[ログ・メトリクス・通知](monitoring.md)を参照。以降の操作は、[日常の操作](operations.md)を参照。
