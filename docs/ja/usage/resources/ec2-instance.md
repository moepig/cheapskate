# EC2 インスタンスの設定

本ドキュメントはEC2 インスタンスの対象条件、グループ設定の適用先、およびタグの設定先を示す。cheapskate のリソースタイプ名は ec2-instance である。

## 対象条件

Auto Scaling グループに所属していない EC2 インスタンスが対象である。AWS 側で起動・停止可能な構成である必要がある。

## DynamoDB と CLI

EC2 インスタンス固有の DynamoDB 属性はない。所属グループのスケジュールと override が、EC2 インスタンスの起動・停止を制御する。インスタンス数やインスタンスタイプを変更する設定はない。

適用する属性は[グループ設定の DynamoDB 登録形式](../group_config.md#dynamodb-の登録形式) を参照。登録・変更例は[CLI 設定例](../cli_records.md) を参照。

## タグ側の設定

cheapskate:group は EC2 インスタンス自身に設定する。EBS volume や起動テンプレートのタグは、対象インスタンスの所属設定の代わりにはならない。AWS が付与する aws:autoscaling:groupName タグがあるインスタンスは、cheapskate:group があっても操作しない。

グループ dev に所属させるため、インスタンス i-0123456789abcdef0 にタグを付けるコマンドを、以下に示す。

```sh
aws ec2 create-tags --region ap-northeast-1 \
  --resources i-0123456789abcdef0 \
  --tags Key=cheapskate:group,Value=dev
```

接続先とグループ名の前提は、[CLI 設定例の前提](../cli_records.md#前提) を参照。タグ操作の引数の詳細は[AWS CLI create-tags](https://docs.aws.amazon.com/cli/latest/reference/ec2/create-tags.html) を参照。

## 設定の反映

EC2 のインスタンス状態 running を稼働中、stopped を停止済みとして扱う。pending、stopping、および shutting-down では操作せず、後続サイクルで再確認する。terminated は見つからない状態として扱い、再起動しない。起動は StartInstances、停止は StopInstances による操作となる。

Auto Scaling グループ所属のエラーがある場合は、そのインスタンスを cheapskate の管理対象から外す。

設定と現在状態の確認方法は、[日常の操作](../operations.md#設定と現在状態の確認) を参照。
