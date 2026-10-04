# Terraform によるレコード登録

本ドキュメントは、schedule、無期限 override、および期限付き override を持つグループアイテムを、Terraform で DynamoDB に登録する例を示す。

## 前提

既存の DynamoDB テーブル cheapskate を使用する。キーは String 型の pk と sk であり、TTL は設定しない。reconciler の DEFAULT_TIMEZONE は Asia/Tokyo とする。Lambda、定期呼び出し、およびリソースの所属タグは設定済みとする。準備の詳細は、[セットアップ](setup.md)を参照。

Terraform はアイテムを直接書き込むため、CLI のグループ検証と条件付き書き込みを実行しない。以下の例では Terraform だけで対象アイテムを変更し、CLI と Web コンソールは参照に使用する。

> [!IMPORTANT]
> 同じアイテムを CLI・Web コンソールと Terraform の両方で変更してはいけない。Terraform がアイテムを書き直すと、設定ファイルにない override などの属性が失われる可能性がある。

## 登録例

以下の main.tf は、未登録の 5 グループのアイテムを作成する。dev-extended は、dev と独立した期限付き override の例である。各グループで制御するリソースには、cheapskate:group=<グループ名> タグを付けること。

```hcl
terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  region = "ap-northeast-1"
}

data "aws_dynamodb_table" "state" {
  name = "cheapskate"
}

variable "override_expires_at" {
  type        = number
  description = "Expiry of the dev-extended override, in Unix seconds."

  validation {
    condition = (
      var.override_expires_at > 0 &&
      var.override_expires_at <= 253402300799 &&
      floor(var.override_expires_at) == var.override_expires_at
    )
    error_message = "Specify a positive integer no greater than 253402300799."
  }
}

locals {
  groups = {
    dev = {
      start_cron    = { S = "0 9 * * 1-5" }
      stop_cron     = { S = "0 18 * * 1-5" }
      ecs_max_count = { N = "4" }
    }
    always-on = {
      override      = { S = "running" }
      ecs_max_count = { N = "4" }
    }
    always-off = {
      override = { S = "stopped" }
    }
    unmanaged = {
      override = { S = "disabled" }
    }
    dev-extended = {
      start_cron          = { S = "0 9 * * 1-5" }
      stop_cron           = { S = "0 18 * * 1-5" }
      override            = { S = "running" }
      override_expires_at = { N = tostring(var.override_expires_at) }
      ecs_max_count       = { N = "4" }
    }
  }
}

resource "aws_dynamodb_table_item" "group" {
  for_each = local.groups

  table_name = data.aws_dynamodb_table.state.name
  hash_key   = data.aws_dynamodb_table.state.hash_key
  range_key  = data.aws_dynamodb_table.state.range_key

  item = jsonencode(merge(each.value, {
    pk = { S = "CONFIG" }
    sk = { S = "GROUP#${each.key}" }
  }))
}
```

item は DynamoDB の属性型を含む JSON で指定する。Number 型も N の値には文字列を渡す。Terraform の引数の詳細は、[aws_dynamodb_table_item](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/dynamodb_table_item)および[aws_dynamodb_table データソース](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/dynamodb_table)を参照。

ecs_max_count は、所属する各 ECS サービスの desired count と scaling-max の上限である。ECS を起動するグループには、タグで指定する台数以上の正の int32 を登録する必要がある。この例では、起動する 3 グループに上限 4 を設定する。未登録またはタグ値の超過は起動エラーとなる。上限はグループ全体の合計台数には適用しない。

登録するアイテムと設定の対応を、以下に示す。すべて pk=CONFIG である。

| sk | 設定 | 所属リソースの動作 |
| --- | --- | --- |
| GROUP#dev | schedule と ECS 上限 4 | 平日 09:00～18:00 に起動状態を維持する |
| GROUP#always-on | running override と ECS 上限 4 | 起動状態を維持する |
| GROUP#always-off | stopped override のみ | 停止状態を維持する |
| GROUP#unmanaged | disabled override のみ | 自動制御を無効にする |
| GROUP#dev-extended | schedule、期限付き running override、ECS 上限 4 | 期限までは起動状態を維持し、失効後は schedule に従う |

## 適用と確認

有効期限は、適用時点より未来の Unix time 秒を terraform.tfvars に指定する。2026-09-14 20:00:00 日本時間の記述例を示す。実際の適用時には目的の日時に置き換えること。

```hcl
override_expires_at = 1789383600
```

AWS 認証情報を設定した環境で、適用内容を確認してから登録する手順を示す。

```sh
terraform init
terraform fmt -check
terraform validate
terraform plan -out=tfplan
terraform apply tfplan
```

CLI で保存された設定と AWS の現在状態を確認する例を、以下に示す。

```sh
export AWS_REGION=ap-northeast-1
export DEFAULT_TIMEZONE=Asia/Tokyo
cheapskate-cli -table cheapskate -output json list
cheapskate-cli -table cheapskate -output json show --group dev-extended
```

apply は設定の登録で完了する。AWS の起動・停止は、その設定を読んだ reconciler のサイクルで行う。反映のタイミングとリソースごとの動作は、[CLI によるレコード登録と AWS の動作](cli_records.md)を参照。

## 設定変更と削除

アイテムの変更時にも、グループとして有効な属性の組み合わせを維持する必要がある。変更方法と制約は、次のとおりである。

- schedule は start_cron と stop_cron を必ず対で指定する。
- 無期限 override は override_expires_at を省略する。0 や NULL は保存しない。
- 期限付き override は schedule と組み合わせる。stopped と disabled も指定できる。
- schedule と無期限 override の併用は、dev-extended から override_expires_at を省いた形で登録する。
- override の解除は、schedule を残して override と override_expires_at を削除し、apply する。
- 失効済みの override 属性は自動削除されない。Terraform にも残っていれば、その後の apply でも失効時刻は変わらない。
- グループを local.groups から削除して apply すると、対応するアイテムが削除される。所属する AWS リソースは削除・停止されない。

グループ名、cron、属性型などの制約は、[基本概念](concepts.md)と[DynamoDB データモデル](../architecture/database.md)を参照。管理を終了する場合は、[操作方法の管理終了手順](operations.md#安全に管理を終了する手順)に従い、設定変更とグループ削除を Terraform で行う。
