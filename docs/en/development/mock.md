# Mocks

This document covers the two forms of test double and when each is used, the generation steps, and how to add one.

Test doubles come in two forms, chosen by layer. The criterion is how wide the boundary is; a narrow boundary never gets a generated mock. The subjects and forms are collected below.

| | Subject | Form | Location |
|---|---|---|---|
| AWS SDK boundary | `internal/state`, `internal/aws/{compute,tagging,sns}`, `internal/devtools/devseed` | Generated with [go.uber.org/mock](https://github.com/uber-go/mock) (gomock) | `mocks/` directly under each package |
| Application-layer ports | `Discoverer`/`Target`/`Describer`/`Notifier` in `internal/app/port` | Hand-written | `internal/app/port/porttest` |

Assertions use [testify](https://github.com/stretchr/testify) (`assert` / `require`) throughout.

## Generated — the AWS SDK boundary

For each package declaring an interface, a `//go:generate` sits next to the definition and generates into the `mocks/` subpackage directly below. An example is given below.

```go
//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks cheapskate/internal/aws/compute RdsAPI,EcsAPI,AutoScalingAPI,Ec2API
```

Regeneration runs from a `make` target.

```console
make generate     # go generate ./... — regenerates mocks/ in every package after an interface change
```

The conventions for generation are collected below.

| Convention | Content |
|---|---|
| No installation required | mockgen is managed through the `tool` directive in go.mod, so `go tool mockgen` just works and the version is pinned there too |
| `-typed` is mandatory | `EXPECT()` returns typed values, so a wrong signature in `Return` / `DoAndReturn` becomes a compile error. It roughly doubles the generated code, which is acceptable since nobody reads it |
| Generated code is committed | CI does not regenerate. After changing a mocked interface, run `make generate` and include the diff in the same commit |

Generation is used at this layer because the subject is an AWS SDK client interface. There are many methods and the arguments and return values are large types, which makes hand-writing costly. That one call is one line, and that the number of calls is verified along the way, are reasons too.

An example of using a generated mock is given below.

```go
ctrl := gomock.NewController(t)
c := mocks.NewMockRdsAPI(ctrl)
c.EXPECT().DescribeDBInstances(gomock.Any(), gomock.Any()).Return(nil, &types.DBInstanceNotFoundFault{})
tgt := &RdsInstanceTarget{Client: c}
```

### The exception: `internal/state/mocks/dynastore.go`

It sits in the same `mocks` package but is hand-written. It connects the generated `MockAPI` to an in-memory table and gives it the same behaviour as real Scan/GetItem/PutItem/UpdateItem/DeleteItem. Its handles for operating on the table are given below.

```go
api, db := mocks.NewDynaStore(ctrl)      // pass api to state.New and operate the table through db
db.Seed(item)                            // plant an item
db.Item("status#rds-instance#db1")        // read back what was written
db.FailOn("update", pk, err)              // fail one operation on one pk
db.SetScanPageSize(n)                     // reproduce Scan pagination
```

## Hand-written — the application-layer ports

These live in `internal/app/port/porttest` and are shared by the packages under `internal/app` and `internal/ui`. Nothing in that package is generated.

The ports are small — four interfaces and seven methods in total, taking only cheapskate's own `model` types — and what the tests want is stateful behaviour (planting observations, recording stop/start) rather than call expectations. A generated mock would end up wrapped in `AnyTimes().DoAndReturn(...)`, leaving nothing but gomock boilerplate.

An example of using a hand-written double is given below.

```go
tgt := porttest.NewTarget(model.TypeRdsInstance)
tgt.Observations["dev-db"] = model.Observation{State: model.StateRunning}
// ... run ...
assert.Equal(t, []string{"dev-db"}, tgt.Stopped)
```

The handles each double offers for planting, recording, and injecting failures are collected below.

| Double | Planting | Recording | Failure injection |
|---|---|---|---|
| `Target` | `Observations` (unset means `StateNotFound`) | `Stopped` / `Started` | `DescribeErr` / `StopErr` / `StartErr` |
| `Discoverer` | `Resources`, with `ByTagValue` overriding per selector tag value | `Selectors` / `Calls()` | `Err` / `ErrByTagValue` |
| `Describer` | `Obs` (a value type, so it goes straight into a map literal) | none | `Err` |
| `Notifier` | none | `Published` | `Err` |

What a generated mock's `EXPECT` verifies implicitly has to be written out with a hand-written double. The equivalents are given below.

```go
assert.Equal(t, []model.Selector{devSelector}, d.Selectors)  // argument match (was: EXPECT(gomock.Any(), devSelector))
assert.Zero(t, d.Calls())                                     // never called (was: writing no EXPECT)
```

## Adding a mock

For an AWS SDK client, or any comparably wide boundary, add a `//go:generate` line next to the declaration and run `make generate`. The line takes the following form.

```go
//go:generate go tool mockgen -typed -destination mocks/mocks.go -package mocks <import path> <interface names, comma-separated>
```

For a small port dealing only in cheapskate's own types, add a hand-written double to `porttest`. A `var _ port.Xxx = (*Xxx)(nil)` pins interface satisfaction at compile time.
