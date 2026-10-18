# IAM 権限

本ページでは cheapskate の reconciler、CLI、および Web コンソールに付与する IAM 権限を説明する。AWS リソースの配置先と用途は、[AWS リソースと IAM](aws_resources.md#配置する-aws-リソース)を参照。

## ポリシー例の前提

以下は同一アカウント・同一リージョンの既存リソースを管理するロールに付与するポリシー例である。リソースの作成、イメージの登録、およびタグの付与は配置時の権限で行う。

PARTITION、REGION、ACCOUNT は配置先に置き換える。通常の AWS パーティションは aws である。

TABLE は STATE_TABLE_NAME、GROUP は許可する所属タグ値とする。

DB_INSTANCE、DB_CLUSTER、CLUSTER、SERVICE、INSTANCE_ID は実際の識別子に置き換える。SCALABLE_TARGET_ID は登録済み scalable target の ARN の末尾である。

FUNCTION_NAME と TOPIC は対象の Lambda 関数名と通知先 topic 名に置き換える。

IAM ポリシーの Resource には対象 ARN を列挙する。複数ある場合は配列にする。所属タグの StringEquals には許可するグループ名を列挙する。所属タグ条件を持つ Statement では、ARN が一致してもタグが削除されたり許可外のグループへ変更されたりするとアクセスを許可しない。

異なるグループに異なる ARN を許可する場合は Statement を分ける。

役割ごとに組み合わせる権限を、以下に示す。

| 権限 | 対象ロール | 必要となる条件 |
| --- | --- | --- |
| [共通のリソース参照](#共通のリソース参照権限) | reconciler、Web コンソール | 常時 |
| [共通のリソース参照](#共通のリソース参照権限) | CLI | show で状態を表示する場合 |
| [設定操作](#cli-と-web-コンソールの権限) | CLI、Web コンソール | 常時 |
| [設定読み取りとリソース更新](#reconciler-の権限) | reconciler | 常時 |
| [ログ配信](#lambda-のログ配信) | 各 Lambda | 関数ごとに付与 |
| [SNS 通知](#sns-通知) | reconciler | 通知を使う場合 |
| [暗号化](#sns-通知と暗号化の追加権限) | reconciler | 対象機能で暗号化を使う場合 |

JSON 全体の例はそれぞれ単独のポリシーとして使用できる。Statement 単体の例は対象ロールのポリシーの Statement 配列へ追加する。

管理しないリソース種別のアクションと Statement は削除する。ECS を管理する場合は、scalable target が未登録の service でも DescribeScalableTargets が必要である。RegisterScalableTarget は登録済みの対象を更新する場合だけ必要となる。

これらは Allow ポリシーであり、他のポリシーが与える権限を取り消さない。最小権限にするには、同じロールに付いた広い Allow も除去する必要がある。DynamoDB のグループ設定、所属タグ、および IAM の対象 ARN を一致させること。IAM で拒否されたリソースは、探索結果から自動的に除外されるわけではない。

## アクションごとのリソースと条件

実行時に必要なアクションと、この構成で使う制限を以下にまとめる。Resource 列は ARN のリソース部を示す。「*」はリソース単位の指定がないことを示す。

| サービス | IAM アクション | Resource のリソース部 | Condition または制限 |
| --- | --- | --- | --- |
| DynamoDB | [dynamodb:Query](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-Query) | table/TABLE | dynamodb:LeadingKeys=CONFIG |
| DynamoDB | [dynamodb:GetItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-GetItem) | table/TABLE | dynamodb:LeadingKeys=CONFIG |
| DynamoDB | [dynamodb:PutItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-PutItem) | table/TABLE | dynamodb:LeadingKeys=CONFIG |
| DynamoDB | [dynamodb:UpdateItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-UpdateItem) | table/TABLE | dynamodb:LeadingKeys=CONFIG |
| DynamoDB | [dynamodb:DeleteItem](https://docs.aws.amazon.com/service-authorization/latest/reference/list_dynamodb.html#list_dynamodb-action-DeleteItem) | table/TABLE | dynamodb:LeadingKeys=CONFIG |
| Resource Groups Tagging API | [tag:GetResources](https://docs.aws.amazon.com/service-authorization/latest/reference/list_resourcegroupstaggingapi.html#list_resourcegroupstaggingapi-action-GetResources) | * | aws:RequestedRegion |
| RDS DB instance | [rds:DescribeDBInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-DescribeDBInstances) | db:DB_INSTANCE | aws:ResourceTag/cheapskate:group |
| RDS DB instance | [rds:StartDBInstance](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StartDBInstance) | db:DB_INSTANCE | aws:ResourceTag/cheapskate:group |
| RDS DB instance | [rds:StopDBInstance](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StopDBInstance) | db:DB_INSTANCE | aws:ResourceTag/cheapskate:group |
| RDS DB cluster の参照 | [rds:DescribeDBClusters](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-DescribeDBClusters) | * | aws:RequestedRegion |
| RDS DB cluster の操作 | [rds:StartDBCluster](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StartDBCluster) | cluster:DB_CLUSTER | aws:ResourceTag/cheapskate:group |
| RDS DB cluster の操作 | [rds:StopDBCluster](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StopDBCluster) | cluster:DB_CLUSTER | aws:ResourceTag/cheapskate:group |
| ECS | [ecs:DescribeServices](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ecs.html#list_ecs-action-DescribeServices) | service/CLUSTER/SERVICE | aws:ResourceTag/cheapskate:group |
| ECS | [ecs:UpdateService](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ecs.html#list_ecs-action-UpdateService) | service/CLUSTER/SERVICE | aws:ResourceTag/cheapskate:group |
| Application Auto Scaling | [application-autoscaling:DescribeScalableTargets](https://docs.aws.amazon.com/service-authorization/latest/reference/list_application-autoscaling.html#list_application-autoscaling-action-DescribeScalableTargets) | * | aws:RequestedRegion |
| Application Auto Scaling | [application-autoscaling:RegisterScalableTarget](https://docs.aws.amazon.com/service-authorization/latest/reference/list_application-autoscaling.html#list_application-autoscaling-action-RegisterScalableTarget) | scalable-target/SCALABLE_TARGET_ID | Resource の scalable target ARN で限定 |
| EC2 | [ec2:DescribeInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-DescribeInstances) | * | aws:RequestedRegion |
| EC2 | [ec2:StartInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-StartInstances) | instance/INSTANCE_ID | aws:ResourceTag/cheapskate:group |
| EC2 | [ec2:StopInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-StopInstances) | instance/INSTANCE_ID | aws:ResourceTag/cheapskate:group |
| SNS | [sns:Publish](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sns.html#list_sns-action-Publish) | TOPIC | Resource の topic ARN で限定 |
| CloudWatch Logs | [logs:CreateLogStream](https://docs.aws.amazon.com/service-authorization/latest/reference/list_logs.html#list_logs-action-CreateLogStream) | log-group:/aws/lambda/FUNCTION_NAME:log-stream:* | Resource のロググループ名で限定 |
| CloudWatch Logs | [logs:PutLogEvents](https://docs.aws.amazon.com/service-authorization/latest/reference/list_logs.html#list_logs-action-PutLogEvents) | log-group:/aws/lambda/FUNCTION_NAME:log-stream:* | Resource のロググループ名で限定 |

rds:DescribeDBClusters は resource ID からクラスタを解決するため、クラスタ名を指定しない照会を使う。

## 共通のリソース参照権限

reconciler、CLI の show、および Web コンソールのグループ詳細に付与する参照ポリシーを、以下に示す。

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReadRegionalInventory",
      "Effect": "Allow",
      "Action": [
        "tag:GetResources",
        "ec2:DescribeInstances",
        "rds:DescribeDBClusters",
        "application-autoscaling:DescribeScalableTargets"
      ],
      "Resource": "*",
      "Condition": {
        "StringEquals": {
          "aws:RequestedRegion": "REGION"
        }
      }
    },
    {
      "Sid": "DescribeManagedDbInstances",
      "Effect": "Allow",
      "Action": "rds:DescribeDBInstances",
      "Resource": "arn:PARTITION:rds:REGION:ACCOUNT:db:DB_INSTANCE",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    },
    {
      "Sid": "DescribeManagedEcsServices",
      "Effect": "Allow",
      "Action": "ecs:DescribeServices",
      "Resource": "arn:PARTITION:ecs:REGION:ACCOUNT:service/CLUSTER/SERVICE",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    }
  ]
}
```

tag:GetResources はリソース ARN とタグ条件によるアクセス制御に対応しない。cheapskate が送る TagFilters と ResourceTypeFilters は検索条件であり、IAM による制限ではない。ec2:DescribeInstances もインスタンス ARN とタグで制限できない。application-autoscaling:DescribeScalableTargets は namespace・dimension の条件キーにも対応しない。

この 3 アクションと rds:DescribeDBClusters は aws:RequestedRegion で呼び出し先リージョンを制限する。同じリージョン内では、管理対象以外の情報も参照可能である。詳細は[aws:RequestedRegion](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition-keys.html#condition-keys-requestedregion) を参照。

RDS DB インスタンスの Describe は、DB 識別子を指定するこの構成では対象 ARN と所属タグで制限できる。RDS DB クラスタの探索では、名前を指定した照会に加え、[DescribeDBClusters の db-cluster-resource-id フィルタ](https://docs.aws.amazon.com/AmazonRDS/latest/APIReference/API_DescribeDBClusters.html)による照会を使用する。この照会を許可するため、参照権限の例は Resource を * とする。クラスタの起動・停止は対象 ARN と所属タグで制限する。ECS の参照と更新は長形式の service ARN で cluster と service を特定し、service 自身の所属タグを条件にする。

## reconciler の権限

共通のリソース参照権限に加えて付与する、設定の読み取りと管理対象の更新ポリシーを、以下に示す。

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReadGroupConfiguration",
      "Effect": "Allow",
      "Action": "dynamodb:Query",
      "Resource": "arn:PARTITION:dynamodb:REGION:ACCOUNT:table/TABLE",
      "Condition": {
        "ForAllValues:StringEquals": {
          "dynamodb:LeadingKeys": [
            "CONFIG"
          ]
        }
      }
    },
    {
      "Sid": "ControlManagedDbInstances",
      "Effect": "Allow",
      "Action": [
        "rds:StartDBInstance",
        "rds:StopDBInstance"
      ],
      "Resource": "arn:PARTITION:rds:REGION:ACCOUNT:db:DB_INSTANCE",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    },
    {
      "Sid": "ControlManagedDbClusters",
      "Effect": "Allow",
      "Action": [
        "rds:StartDBCluster",
        "rds:StopDBCluster"
      ],
      "Resource": "arn:PARTITION:rds:REGION:ACCOUNT:cluster:DB_CLUSTER",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    },
    {
      "Sid": "UpdateManagedEcsServices",
      "Effect": "Allow",
      "Action": "ecs:UpdateService",
      "Resource": "arn:PARTITION:ecs:REGION:ACCOUNT:service/CLUSTER/SERVICE",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    },
    {
      "Sid": "UpdateManagedScalableTargets",
      "Effect": "Allow",
      "Action": "application-autoscaling:RegisterScalableTarget",
      "Resource": "arn:PARTITION:application-autoscaling:REGION:ACCOUNT:scalable-target/SCALABLE_TARGET_ID"
    },
    {
      "Sid": "ControlManagedEc2Instances",
      "Effect": "Allow",
      "Action": [
        "ec2:StartInstances",
        "ec2:StopInstances"
      ],
      "Resource": "arn:PARTITION:ec2:REGION:ACCOUNT:instance/INSTANCE_ID",
      "Condition": {
        "StringEquals": {
          "aws:ResourceTag/cheapskate:group": [
            "GROUP"
          ]
        }
      }
    }
  ]
}
```

### DynamoDB

reconciler はテーブルの CONFIG パーティションだけを Query する。index の ARN、Scan、および書き込み権限は不要である。

dynamodb:LeadingKeys は複数値の条件キーであるため ForAllValues:StringEquals を使用し、CONFIG 以外のパーティションへのアクセスを許可しない。この文書で許可する Query、GetItem、PutItem、UpdateItem、DeleteItem はいずれもパーティションキーの指定を必要とする。詳細は[DynamoDB の条件キー](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/specifying-conditions.html)と [Query のキー条件](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_Query.html)を参照。

LeadingKeys が制限するのは pk であり、sk=GROUP#... ではない。全グループが同じ CONFIG パーティションにあるため、この条件ではグループ単位の隔離はできない。

### RDS と EC2

RDS は DB instance と DB cluster を別 Statement にする。StopDBInstance ではスナップショットを要求しないため、snapshot ARN とスナップショット作成権限は付与しない。対応するリソースの詳細は、[StopDBInstance](https://docs.aws.amazon.com/service-authorization/latest/reference/list_rds.html#list_rds-action-StopDBInstance) を参照。

EC2 の起動・停止は列挙した instance ARN と所属タグの両方で制限する。

起動時の認可に License Manager の license configuration や Dedicated Host の resource group を使用する構成がある。その場合は、StartInstances の追加リソースとして該当 ARN だけを別 Statement で許可する。インスタンスのタグ条件をその Statement にコピーしてはいけない。対応するリソースの詳細は、[StartInstances](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#list_ec2-action-StartInstances)と [License Manager を使用する場合の権限例](https://docs.aws.amazon.com/solutions/latest/instance-scheduler-on-aws/security-1.html#ec2-license-manager) を参照。

### ECS と Application Auto Scaling

RegisterScalableTarget の許可ポリシーは、Resource に Application Auto Scaling が返す ScalableTargetARN を指定する。ECS service ARN は指定しない。API の ResourceId=service/CLUSTER/SERVICE を scalable-target/ の後ろへ連結してはいけない。配置時に ARN を確認するコマンドを、以下に示す。

```console
aws application-autoscaling describe-scalable-targets \
  --region REGION \
  --service-namespace ecs \
  --scalable-dimension ecs:service:DesiredCount \
  --resource-ids service/CLUSTER/SERVICE \
  --query 'ScalableTargets[].ScalableTargetARN' \
  --output text
```

scalable target は ResourceId、ServiceNamespace、ScalableDimension の組み合わせで一意に決まる。上記のコマンドで取得した ARN を Resource に指定すれば、対象 service の desired count を管理する scalable target に限定できる。再作成で ARN が変わった場合は IAM ポリシーも更新する。

scalable target と AWSServiceRoleForApplicationAutoScaling_ECSService は配置時に準備する。reconciler は RoleARN や Tags を渡さずに既存 target を更新する。この用途では次の権限を追加しない。

- iam:PassRole
- iam:CreateServiceLinkedRole
- application-autoscaling:TagResource

前提となる挙動の詳細は[RegisterScalableTarget のパラメータ](https://docs.aws.amazon.com/autoscaling/application/APIReference/API_RegisterScalableTarget.html)と [Application Auto Scaling のサービスリンクロール](https://docs.aws.amazon.com/autoscaling/application/userguide/application-auto-scaling-service-linked-roles.html) を参照。

ecs:UpdateService には desiredCount のみの変更に限定する条件キーがない。RegisterScalableTarget にも MinCapacity・MaxCapacity・SuspendedState の値を限定する条件キーがない。ARN とタグで操作対象を制限しても、これらの値に対するアプリケーションの検証を IAM で再現できるわけではない。

## CLI と Web コンソールの権限

設定の読み書きに使用するポリシーを、以下に示す。リソースの状態を表示する場合は、共通のリソース参照権限も付与する。

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ManageGroupConfiguration",
      "Effect": "Allow",
      "Action": [
        "dynamodb:Query",
        "dynamodb:GetItem",
        "dynamodb:PutItem",
        "dynamodb:UpdateItem",
        "dynamodb:DeleteItem"
      ],
      "Resource": "arn:PARTITION:dynamodb:REGION:ACCOUNT:table/TABLE",
      "Condition": {
        "ForAllValues:StringEquals": {
          "dynamodb:LeadingKeys": [
            "CONFIG"
          ]
        }
      }
    }
  ]
}
```

閲覧だけに使用する場合は、PutItem、UpdateItem、および DeleteItem を除く。Terraform で設定を管理し CLI を確認だけに使う場合も同様である。管理インターフェースには reconciler のリソース更新権限と SNS 通知権限を付与しない。

DynamoDB の設定変更は次の reconciler サイクルの起動・停止要求を変更する。この設定変更権限は管理対象の運転状態を変更できる権限として扱う。

## Lambda のログ配信

ロググループは配置時に作成する。各 Lambda の実行ロールにはその関数のログストリームを作成して書き込む権限だけを付与する。Statement の例を、以下に示す。

```json
{
  "Sid": "WriteFunctionLogs",
  "Effect": "Allow",
  "Action": [
    "logs:CreateLogStream",
    "logs:PutLogEvents"
  ],
  "Resource": "arn:PARTITION:logs:REGION:ACCOUNT:log-group:/aws/lambda/FUNCTION_NAME:log-stream:*"
}
```

FUNCTION_NAME はロールごとに対応する関数名とする。独自のロググループを使用する場合はその名前に置き換える。ログストリーム名は実行時に生成されるため、末尾だけをワイルドカードにする。事前作成したロググループを使う構成では logs:CreateLogGroup は不要である。Lambda のログ配信の詳細は[CloudWatch Logs への送信](https://docs.aws.amazon.com/lambda/latest/dg/monitoring-cloudwatchlogs.html) を参照。

cheapskate のメトリクスはログ中の EMF から生成するため、cloudwatch:PutMetricData は不要である。

## SNS 通知と暗号化の追加権限

この節の Statement は使用する構成にだけ追加する。KMS を使用する場合は、IAM ポリシーに加えて対象キーのキーポリシーでもアクセスが許可されている必要がある。

### SNS 通知

NOTIFICATION_TOPIC_ARN を設定する場合に、reconciler へ付与する Statement を、以下に示す。

```json
{
  "Sid": "PublishActionNotifications",
  "Effect": "Allow",
  "Action": "sns:Publish",
  "Resource": "arn:PARTITION:sns:REGION:ACCOUNT:TOPIC"
}
```

Resource は NOTIFICATION_TOPIC_ARN と一致させる。通知が無効ならこの Statement は不要である。

### SNS の暗号化

暗号化した topic への発行では対象 KMS キーの [kms:GenerateDataKey](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html#list_kms-action-GenerateDataKey) と [kms:Decrypt](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html#list_kms-action-Decrypt) を許可する。SNS_KEY_ID は topic が使用する KMS キーの ID とする。SNS 経由かつ対象 topic の暗号化コンテキストに限定する Statement を、以下に示す。

```json
{
  "Sid": "UseNotificationKey",
  "Effect": "Allow",
  "Action": [
    "kms:GenerateDataKey",
    "kms:Decrypt"
  ],
  "Resource": "arn:PARTITION:kms:REGION:ACCOUNT:key/SNS_KEY_ID",
  "Condition": {
    "StringEquals": {
      "kms:ViaService": "sns.REGION.amazonaws.com",
      "kms:EncryptionContext:aws:sns:topicArn": "arn:PARTITION:sns:REGION:ACCOUNT:TOPIC"
    }
  }
}
```

必要な権限と暗号化コンテキストの詳細は、[SNS のキー管理](https://docs.aws.amazon.com/sns/latest/dg/sns-key-management.html)と [SNS の暗号化設定](https://docs.aws.amazon.com/sns/latest/dg/sns-enable-encryption-for-topic.html) を参照。kms:ViaService のサービス名は、[KMS の条件キー](https://docs.aws.amazon.com/kms/latest/developerguide/conditions-kms.html#conditions-kms-via-service) を参照。

### 暗号化した EBS ボリュームの起動

暗号化した EBS ボリュームを持つ EC2 を起動する場合は、使用する KMS キーへの [kms:CreateGrant](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html#list_kms-action-CreateGrant) も必要となる。EBS_KEY_ID は対象ボリュームの KMS キー ID、EBS_VOLUME_ID は vol- で始まるボリューム ID とする。対象ボリュームの復号用 grant を EBS 経由で作成する Statement を、以下に示す。

```json
{
  "Sid": "CreateEbsStartGrant",
  "Effect": "Allow",
  "Action": "kms:CreateGrant",
  "Resource": "arn:PARTITION:kms:REGION:ACCOUNT:key/EBS_KEY_ID",
  "Condition": {
    "StringEquals": {
      "kms:ViaService": "ec2.REGION.amazonaws.com",
      "kms:EncryptionContext:aws:ebs:id": [
        "EBS_VOLUME_ID"
      ]
    },
    "ForAllValues:StringEquals": {
      "kms:GrantOperations": [
        "Decrypt"
      ]
    }
  }
}
```

kms:ViaService の ec2.REGION.amazonaws.com は EBS 経由の KMS 呼び出しを表す。kms:GrantOperations は Decrypt だけを許可し、kms:EncryptionContext:aws:ebs:id は許可するボリューム ID を限定する。同じキーで暗号化された別のボリュームは、この Statement では許可されない。複数ボリュームがある場合はキーごとに Statement を分け、そのキーを使用する対象ボリュームの ID を列挙する。ボリュームの追加や交換時にはこの一覧も更新する。

CreateGrant の Operations は必須であり、空の操作一覧は API が受け付けない。詳細は[kms:GrantOperations](https://docs.aws.amazon.com/kms/latest/developerguide/conditions-kms.html#conditions-kms-grant-operations)、[EBS の暗号化コンテキスト](https://docs.aws.amazon.com/kms/latest/developerguide/services-ebs.html)、および [EBS の grant 作成例](https://docs.aws.amazon.com/kms/latest/developerguide/ct-ec2two.html)を参照。

## 実行ロールの信頼と定期呼び出し

実行ロールの信頼ポリシーと Lambda の呼び出し権限は、前述のリソース操作権限とは別に設定する。[sts:AssumeRole](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sts.html#list_sts-action-AssumeRole) と [lambda:InvokeFunction](https://docs.aws.amazon.com/service-authorization/latest/reference/list_lambda.html#list_lambda-action-InvokeFunction) の用途を、以下にまとめる。

### Lambda の実行ロール

reconciler と Web Lambda の実行ロールには、関数ごとに専用のロールを使用する。信頼ポリシーでは Principal.Service=lambda.amazonaws.com に sts:AssumeRole を許可する。

### EventBridge Scheduler の呼び出し

呼び出し用ロールでは lambda:InvokeFunction を許可する。Resource は呼び出す reconciler 関数または alias の完全な ARN に限定する。信頼ポリシーでは Principal.Service=scheduler.amazonaws.com に sts:AssumeRole を許可する。

条件は ArnEquals とする。aws:SourceArn は arn:PARTITION:scheduler:REGION:ACCOUNT:schedule-group/SCHEDULE_GROUP にする。

### EventBridge rule の呼び出し

Lambda リソースポリシーで Principal.Service=events.amazonaws.com に lambda:InvokeFunction を許可する。対象関数 ARN に限定する。

ArnLike の aws:SourceArn は arn:PARTITION:events:REGION:ACCOUNT:rule/RULE_NAME にする。

aws:SourceArn にはアカウント ID を含む完全な ARN を指定する。この場合、呼び出し元アカウントも ARN で制限される。詳細は[aws:SourceArn](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition-keys.html#condition-keys-sourcearn) を参照。

Scheduler の aws:SourceArn は schedule group ARN であり、個別の schedule ARN ではない。詳細は[Scheduler の信頼ポリシー条件](https://docs.aws.amazon.com/scheduler/latest/UserGuide/cross-service-confused-deputy-prevention.html) を参照。EventBridge rule と Lambda 実行ロールの設定は、[EventBridge の Lambda 呼び出し権限](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-use-resource-based.html#eb-lambda-permissions)と [Lambda の実行ロール](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html) を参照。
