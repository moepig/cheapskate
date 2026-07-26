# モック

テストダブルは層によって 2 種類を使い分ける。判断基準は境界の広さであり、狭い境界に生成を使わない。

| | 対象 | 方式 | 置き場所 |
|---|---|---|---|
| AWS SDK 境界 | `internal/state`、`internal/aws/{compute,tagging,sns}`、`internal/devtools/devseed` | [go.uber.org/mock](https://github.com/uber-go/mock)(gomock)で生成 | 各パッケージ直下の `mocks/` |
| アプリ層のポート | `internal/app/port` の `Discoverer`/`Target`/`Describer`/`Notifier` | 手書き | `internal/app/port/porttest` |

アサーションは全体で [testify](https://github.com/stretchr/testify)(`assert` / `require`)を使う。

## 生成 — AWS SDK 境界

インターフェースを宣言するパッケージごとに、定義の隣に `//go:generate` を置き、直下の `mocks/` サブパッケージへ生成する。

```go
//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks cheapskate/internal/aws/compute RdsAPI,EcsAPI,AutoScalingAPI,Ec2API
```

```console
make generate     # go generate ./... — インターフェース変更後に全パッケージの mocks/ を再生成
```

| 規約 | 内容 |
|---|---|
| インストールを要しない | mockgen は go.mod の `tool` ディレクティブで管理するため、`go tool mockgen` がそのまま動く。バージョンも go.mod に固定される |
| `-typed` を必須とする | `EXPECT()` が型付きの値を返すため、`Return` / `DoAndReturn` のシグネチャ誤りがコンパイルエラーになる。生成コード量は倍近くになるが、読む対象ではないため許容する |
| 生成物はコミットする | CI は再生成しない。モック対象のインターフェースを変更したら `make generate` を実行し、差分を同じコミットに含める |

ここで生成が見合うのは、対象が AWS SDK クライアントのインターフェースだからである。メソッド数が多く、引数と戻り値が大きな型であるため、手書きは割に合わない。呼び出し 1 回を 1 行で書け、呼ばれた回数の検証まで伴う点も効いている。

```go
ctrl := gomock.NewController(t)
c := mocks.NewMockRdsAPI(ctrl)
c.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).Return(nil, &types.DBInstanceNotFoundFault{})
tgt := &RdsInstanceTarget{Client: c}
```

### 例外: `internal/state/mocks/dynastore.go`

同じ `mocks` パッケージ内にあるが手書き。生成された `MockAPI` をインメモリテーブルに接続し、実際の Scan/GetItem/PutItem/UpdateItem/DeleteItem と同じ振る舞いを与える。

```go
api, db := mocks.NewDynaStore(ctrl)      // api を state.New に渡し、db でテーブルを操作する
db.Seed(item)                            // アイテムを仕込む
db.Item("status#rds-instance#db1")        // 書き込み結果を読む
db.FailOn("update", pk, err)              // 特定操作・特定 pk だけ失敗させる
db.SetScanPageSize(n)                     // Scan のページングを再現する
```

## 手書き — アプリ層のポート

`internal/app/port/porttest` に置き、`internal/app` と `internal/ui` の各パッケージで共用する。このパッケージに生成するものはない。

ポートは cheapskate 自身の `model` 型だけを取る 4 インターフェース・計 7 メソッドと小さく、テスト側が求めるのは呼び出し期待値ではなく状態を持つ振る舞い(観測値の仕込み、stop/start の記録)である。生成モックを使っても `AnyTimes().DoAndReturn(...)` で包み直すことになり、gomock の儀式だけが残る。

```go
tgt := porttest.NewTarget(model.TypeRdsInstance)
tgt.Observations["dev-db"] = model.Observation{State: model.StateRunning}
// ... 実行 ...
assert.Equal(t, []string{"dev-db"}, tgt.Stopped)
```

| ダブル | 仕込み | 記録 | 失敗の注入 |
|---|---|---|---|
| `Target` | `Observations`(未設定は `StateNotFound`) | `Stopped` / `Started` | `DescribeErr` / `StopErr` / `StartErr` |
| `Discoverer` | `Resources`、セレクタのタグ値ごとに上書きする `ByTagValue` | `Selectors` / `Calls()` | `Err` / `ErrByTagValue` |
| `Describer` | `Obs`(値型のため map リテラルに直接書ける) | なし | `Err` |
| `Notifier` | なし | `Published` | `Err` |

生成モックの `EXPECT` が暗黙に持つ検証は、手書きダブルでは明示的に書く必要がある。

```go
assert.Equal(t, []model.Selector{devSelector}, d.Selectors)  // 引数の一致(旧: EXPECT(gomock.Any(), devSelector))
assert.Zero(t, d.Calls())                                     // 呼ばれないこと(旧: EXPECT を書かない)
```

## 新しいモックを追加するとき

AWS SDK クライアントか、それに準ずる広い境界の場合は、宣言の隣に `//go:generate` 行を追記して `make generate` を実行する。

```go
//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks <インポートパス> <インターフェース名をカンマ区切り>
```

cheapskate 自身の型だけを扱う小さなポートの場合は、`porttest` に手書きで足す。`var _ port.Xxx = (*Xxx)(nil)` でインターフェース充足をコンパイル時に固定する。
