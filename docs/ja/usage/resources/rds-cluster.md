# Aurora DB クラスタの設定

本ドキュメントは Aurora DB クラスタの対象条件、グループ設定の適用先、およびタグの設定先を示す。cheapskate のリソースタイプ名は rds-cluster である。

## 対象条件

engine が aurora で始まる DB クラスタが対象である。Aurora 以外の RDS DB クラスタは対象外である。AWS 側で起動・停止可能な構成である必要がある。設定と操作の単位はクラスタであり、所属 DB インスタンスに個別の起動・停止設定は行わない。

## DynamoDB と CLI

DB クラスタ固有の DynamoDB 属性はない。所属グループのスケジュールと override が、DB クラスタの起動・停止を制御する。

適用する属性は[グループ設定の DynamoDB 登録形式](../group_config.md#dynamodb-の登録形式) を参照。登録・変更例は[CLI 設定例](../cli_records.md) を参照。

## タグ側の設定

cheapskate:group は DB クラスタ自身に設定する。所属 DB インスタンスへのタグ付けは、クラスタの所属設定の代わりにはならない。

一覧表示と起動・停止には DBClusterIdentifier のクラスタ名を使用する。DbClusterResourceId の cluster- で始まる resource ID はクラスタ名とは異なる。同一クラスタを両方の識別子で検出した場合も、一覧には 1 件だけ表示する。

グループ dev に所属させるため、DB クラスタ dev-cluster にタグを付けるコマンドを、以下に示す。

```sh
aws rds add-tags-to-resource --region ap-northeast-1 \
  --resource-name arn:aws:rds:ap-northeast-1:123456789012:cluster:dev-cluster \
  --tags Key=cheapskate:group,Value=dev
```

接続先とグループ名の前提は、[CLI 設定例の前提](../cli_records.md#前提) を参照。タグ操作の引数の詳細は[AWS CLI add-tags-to-resource](https://docs.aws.amazon.com/cli/latest/reference/rds/add-tags-to-resource.html) を参照。

## 設定の反映

Aurora の DB クラスタ状態 available を稼働中、stopped を停止済みとして扱う。他の状態では操作せず、後続サイクルで再確認する。起動は StartDBCluster、停止は StopDBCluster によるクラスタ単位の操作となる。

所属 DB インスタンスに所属タグを付けた場合、そのインスタンスは対象外のエラーとなる。

設定と現在状態の確認方法は、[日常の操作](../operations.md#設定と現在状態の確認) を参照。
