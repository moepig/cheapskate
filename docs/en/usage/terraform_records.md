# Terraform group configuration

This document shows how to register group items containing schedules, indefinite overrides, and timed overrides through Terraform.

## Prerequisites

Use an existing cheapskate table with String keys pk and sk and no TTL. These examples use reconciler DEFAULT_TIMEZONE=Asia/Tokyo. Prepare Lambda, its periodic trigger, and resource membership tags beforehand. For preparation, see [Setup](setup.md).

Terraform writes items directly and bypasses CLI validation and conditional writes. In these examples, Terraform is the sole writer; the CLI and web console are used for inspection.

> [!IMPORTANT]
> Do not change the same item through both Terraform and the CLI or web console. Rewriting an item through Terraform can remove attributes, such as overrides, that are absent from its configuration.

## Group definitions

This main.tf creates five new groups. dev-extended is independent of dev and demonstrates a timed override. Set cheapskate:group=<group-name> on each group's resources.

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

Item uses DynamoDB JSON including attribute types. Number values also use strings in N. For Terraform arguments, see [aws_dynamodb_table_item](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/dynamodb_table_item) and the [table data source](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/data-sources/dynamodb_table).

The three groups that allow startup have ecs_max_count=4. For ECS this must cover the startup-count and normal-maximum tags. It applies per service rather than to the group's total task count.

All items use pk=CONFIG. Their requests are:

| sk | Configuration | Member behavior |
| --- | --- | --- |
| GROUP#dev | Schedule and ECS limit 4 | Run weekdays 09:00–18:00 |
| GROUP#always-on | running override and ECS limit 4 | Keep running |
| GROUP#always-off | stopped override | Keep stopped |
| GROUP#unmanaged | disabled override | Skip automatic control |
| GROUP#dev-extended | Schedule, timed running override, ECS limit 4 | Keep running until expiration, then follow schedule |

## Applying and inspecting

Set override_expires_at in terraform.tfvars to a future Unix timestamp. This example represents 2026-09-14 20:00:00 in Japan; replace it with the intended future time when applying:

```hcl
override_expires_at = 1789383600
```

With AWS credentials configured, inspect the plan and apply it:

```sh
terraform init
terraform fmt -check
terraform validate
terraform plan -out=tfplan
terraform apply tfplan
```

Inspect saved configuration and current state using the CLI:

```sh
export AWS_REGION=ap-northeast-1
export DEFAULT_TIMEZONE=Asia/Tokyo
cheapskate-cli -table cheapskate -output json list
cheapskate-cli -table cheapskate -output json show --group dev-extended
```

Apply completes when configuration is saved. AWS operations occur in a reconciler cycle that reads it. For timing and resource effects, see [Start and stop behavior](resource_flow.md).

## Changes and removal

Preserve valid group configuration on every update:

- Specify start_cron and stop_cron together.
- Omit override_expires_at for an indefinite override; do not store zero or NULL.
- Combine timed overrides with a schedule, including stopped and disabled overrides.
- To clear an override, retain the schedule, remove override and override_expires_at, and apply.
- Expired override attributes remain until removed; another apply does not extend their time.
- Removing a group from local.groups and applying deletes its item without stopping or deleting AWS resources.

For names, cron, types, and constraints, see [Group configuration](group_config.md). To end management, follow [Daily operations](operations.md#ending-management), using Terraform for configuration changes and deletion.
