# RDS DB インスタンスの設定

本ドキュメントは RDS DB インスタンスの対象条件、グループ設定の適用先、およびタグの設定先を示す。cheapskate のリソースタイプ名は rds-instance である。

## 対象条件

DB クラスタに所属していない、RDS Custom 以外の DB インスタンスが対象である。Aurora の所属 DB インスタンスは個別に管理しない。AWS 側で起動・停止可能な構成である必要がある。

Aurora の設定は[Aurora DB クラスタの設定](rds-cluster.md) を参照。

## DynamoDB と CLI

DB インスタンス固有の DynamoDB 属性はない。所属グループのスケジュールと override が、DB インスタンスの起動・停止を制御する。

適用する属性は[グループ設定の DynamoDB 登録形式](../group_config.md#dynamodb-の登録形式) を参照。登録・変更例は[CLI 設定例](../cli_records.md) を参照。

## タグ側の設定

cheapskate:group は DB インスタンス自身に設定する。

グループ dev に所属させるため、DB インスタンス dev-db にタグを付けるコマンドを、以下に示す。

```sh
aws rds add-tags-to-resource --region ap-northeast-1 \
  --resource-name arn:aws:rds:ap-northeast-1:123456789012:db:dev-db \
  --tags Key=cheapskate:group,Value=dev
```

接続先とグループ名の前提は、[CLI 設定例の前提](../cli_records.md#前提) を参照。タグ操作の引数の詳細は[AWS CLI add-tags-to-resource](https://docs.aws.amazon.com/cli/latest/reference/rds/add-tags-to-resource.html) を参照。

## 設定の反映

RDS の DB インスタンス状態 available を稼働中、stopped を停止済みとして扱う。他の状態では操作せず、後続サイクルで再確認する。起動は StartDBInstance、停止は StopDBInstance による DB インスタンス単位の操作となる。

設定と現在状態の確認方法は、[日常の操作](../operations.md#設定と現在状態の確認) を参照。
