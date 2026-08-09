# Architecture overview

## Design principles

The implementation described here rests on four principles. Each is given below.

| Principle | Content |
|---|---|
| Desired state + reconcile loop | It converges on a state rather than executing commands. There is no waiting on transitions: a resource that is mid-transition is skipped and retried on the next cycle |
| Separation of responsibility | Only the reconciler calls the RDS/ECS/EC2 control APIs. `cheapskate-cli` and the web console reach no further than DynamoDB and the read-only `tag:GetResources` |
| Dynamic discovery through AWS tags | A group holds its configuration and a selector (a tag key/value plus the target resource types), and membership is resolved through the Resource Groups Tagging API on every reconcile. The source of truth is the current value of the AWS tags; individual resources are never registered |
| Images and binaries as the only artifacts | Every release publishes container images to GHCR and CLI archives to the GitHub release. No IaC template is distributed, and no particular means of creating the AWS resources is prescribed |

## Components

There are three executables. Where each runs and what it does are given below.

| Component | Runs on | Role |
|---|---|---|
| reconciler | Lambda | The reconcile loop. The only component that starts or stops AWS resources |
| `cheapskate-cli` | A local machine | Entering and inspecting group configuration, and diagnosing the state table. Nothing is deployed to AWS for it |
| Web console | Lambda (optional) | The same operations as the CLI, from a browser. The reconcile loop is complete without it |

None of the three calls another. They meet only through the state table, and the items the reconciler writes never overlap with those the configuration side writes.

For the design of `cheapskate-cli` see [cheapskate-cli.md](cheapskate-cli.md), and for that of the web console see [web_console.md](web_console.md).

## Data model

The state lives in a single DynamoDB table. There are three kinds of item; their contents and writers are given below.

| Item | Content | Writer |
|---|---|---|
| `group#<name>` | How the group's desired state is decided, and its selector | `cheapskate-cli` / web console / IaC |
| `override#<name>` | A time-limited override of the desired state | Same as above |
| `status#<...>` | The reconciler's results (last action, last error, ongoing transition) | reconciler |

The items written by the configuration side and those written by the reconciler never overlap. Because of that separation, managing `group#` with IaC does not drift against the reconciler's writes. For details, see the key layout, attributes, and read/write matrix in [database.md](database.md).

## Resolving the desired state

The desired state of a group is decided from its configuration and its override. The order of precedence is given below; earlier rows win.

| Precedence | Condition | Resulting state |
|---|---|---|
| 1 | `mode: disabled` | No desired state is decided, and the group is excluded from processing |
| 2 | An unexpired override exists | The override's `desired` |
| 3 | `mode: pinned` | The group's `desired` |
| 4 | `mode: schedule` | The result of evaluating the crons |

Cron evaluation compares the most recent past firing time of `start_cron` and of `stop_cron` and follows whichever fired later. On a tie, stop wins. The timezone used is the group's `timezone`, falling back to the reconciler's `DEFAULT_TIMEZONE`. tzdata is embedded in the binary.

## The reconcile loop

One invocation begins with a single Scan of the whole table and processes every group. The work for one group is as follows.

```
desired = resolve desired state       # if disabled, skip without discovering
resources = discover(selector)        # tag:GetResources
for resource in resources:
    actual = Describe()
    if actual is transitioning:
        skip                          # retried on the next cycle
    elif desired != actual:
        Stop() / Start()
        update status# + notify
```

Three paths invoke the loop: the scheduled run, RDS auto-start events, and manual invocation. None of them changes the scope of the work through its payload. For details, see the invocation paths in [trigger.md](trigger.md).

### Rules

The rules the loop follows, and the reasoning behind each, are collected below.

| Rule | Reasoning |
|---|---|
| A converged cycle writes nothing and notifies nothing | — |
| Errors are isolated per resource, recorded in `last_error` on `status#`, and notified | One failure must not spread to other resources or groups |
| A not-found seen immediately after discovery is a skip, not an error | The Tagging API is eventually consistent, so tag changes take time to appear |
| A transitioning resource is skipped, and `transitioning_since` is written exactly once, on the cycle that first observes the transition | A skip is neither an error nor a notification, and without this there is no way to tell a transition that never ends from a converged resource |
| When several groups' selectors match the same resource, the group that sorts first by name manages it | The owner has to be decided unambiguously |
| The groups that lose the tie record the whole cycle's duplicates as a single entry on their own `status#group#<name>` | It keeps that record separate from the resource-side `status#` that the owning group writes |
| Group-level failures (an invalid cron or timezone, a discovery failure, a selector collision) are recorded on `status#group#<name>` and notified, and are cleared exactly once, after all of that group's work is finished | It avoids the notification flapping that comes from recording and clearing within the same cycle |
| On a cycle with one or more per-resource failures, the loop runs to completion and the handler then returns an error | Whether to swallow a failure and whether to report it to the caller are separate questions |

### Operations per resource type

The APIs called for each type are given below.

| Type | Stop | Start |
|---|---|---|
| `rds-instance` / `rds-cluster` | `StopDBInstance` / `StopDBCluster` | `StartDBInstance` / `StartDBCluster` |
| `ec2-instance` | `StopInstances` | `StartInstances` |
| `ecs-service` | If an Application Auto Scaling target exists, set min/max to 0/0, then `UpdateService(desiredCount=0)` | Restore desiredCount through `UpdateService`, then restore min/max |

For ECS, the desiredCount and the Auto Scaling min/max used at start are read from the resource's own tags. They are not saved on stop and restored later. Whether an Auto Scaling target exists is determined with `DescribeScalableTargets`.

Stopping ECS takes two steps and is not atomic. If `UpdateService` fails after min/max have been set to 0/0, the original min/max are rolled back. Without the rollback the service would be left running and unable to scale out.

## Diagnosing the state table

Inconsistencies in the state table (orphaned records, selector collisions, transitions that never end) are not handled by the reconcile loop. `internal/app/doctor` diagnoses them, and the CLI and the web console share that implementation.

The verdict rests on both a Scan of the whole table and discovery for every group, because whether a record is orphaned is settled by nothing other than the fact that it matches no group's selector. Consequently, a cycle in which even one discovery fails withholds the orphan verdict entirely. That keeps the audit record of a resource that merely could not be discovered for a moment.

Only records whose subject is proven absent by the Scan and the discovery alone may be deleted. Configuration itself, anything requiring human judgement, and the AWS resources are never touched.

The reconciler does not delete orphaned `status#` records for the same reason. Discovery results lag behind through the Tagging API, so the match situation observed mid-reconcile is not grounds for deleting an audit record.

## Repository layout

`internal/` is split into a directory per layer. Dependencies always point inwards (top to bottom in the table below); no import runs the other way. Each layer and what it may import are given below.

| Layer | Packages | May import |
| --- | --- | --- |
| Domain | `core/model`, `core/schedule` | nothing (stdlib only) |
| Persistence | `state` | `core` |
| Application | `app/groups`, `app/reconcile`, `app/doctor`, `app/port` | `core`, `state`, `app/port` |
| AWS adapters | `aws/tagging`, `aws/compute`, `aws/sns`, `aws/cloudwatch` | `core` |
| Frontends | `ui/cli`, `ui/webconsole` | `core`, `state`, `app`, `wire` |
| Composition | `wire` | anything |

```
.
├── cmd/                      # thin mains only; the work lives in internal/ui and internal/wire
│   ├── reconciler/           # Lambda entrypoint (bootstrap)
│   ├── cheapskate-cli/       # configuration CLI
│   ├── webconsole/           # web console (runs on Lambda and locally)
│   └── dev-bootstrap/        # creates the state table for local development (not in the images)
├── internal/
│   ├── core/
│   │   ├── model/            # domain types and validation
│   │   └── schedule/         # resolving the desired state
│   ├── state/                # DynamoDB state table: access layer + key layout + item shapes + schema
│   ├── app/
│   │   ├── port/             # the application layer's outward interfaces (implemented by aws/)
│   │   ├── groups/           # group configuration operations (shared by the CLI and the web console)
│   │   ├── reconcile/        # the reconcile loop
│   │   └── doctor/           # state table diagnosis and orphan pruning (shared by both frontends)
│   ├── aws/
│   │   ├── tagging/          # selector → resource discovery (the only package that calls the Tagging API)
│   │   ├── compute/          # describe/stop/start for rds / ecs / ec2
│   │   ├── sns/              # publishing notifications
│   │   └── cloudwatch/       # EMF metrics as log output (makes no API calls)
│   ├── ui/
│   │   ├── cli/              # the cheapskate-cli implementation
│   │   └── webconsole/       # the web console's HTTP handlers and templates
│   ├── wire/                 # composition root: binds the aws adapters to app/port
│   └── devtools/             # development and test only (not in the Lambda images)
│       ├── devseed/          # dummy ECS resources for local development
│       └── emutest/          # emulator connection helpers
├── tests/                    # tests whose subject is not a single package
│   ├── system/               # the reconcile loop wired to real adapters (`integration` tag)
│   └── image/                # black-box checks of the built container images (`image` tag)
├── Dockerfile                # multi-stage build (cross-compiles)
├── compose.yaml              # the local AWS emulator
└── docs/en, docs/ja/         # documentation
```

### Independence of the application layer from the AWS adapters

What the application layer needs from the outside world is declared as four interfaces in `app/port` (`Discoverer` / `Target` / `Describer` / `Notifier`). The packages under `internal/aws` implement them, and `internal/wire` binds the two. `wire` is the only place that knows which AWS client satisfies which port.

### The place of the state table

The state table is not a pluggable dependency but a structure intrinsic to cheapskate, so it is not a port. The application layer uses `internal/state` directly, and its tests mock the DynamoDB client underneath.

The window onto the state is an interface declaring only what each consumer needs. `reconcile.Store` has no method for writing group configuration or overrides, and `groups.Store` and `doctor.Store` have none for writing status. A method that cannot be called does not exist, so the read/write separation needs no discipline to hold.

The key layout and item shapes are likewise closed inside `internal/state`. `core/model` holds domain types only and knows neither how a `pk` is assembled nor anything about `dynamodbav` tags. The application layer never assembles a key string; it calls methods named for intent.

### Named types for the domain vocabulary

`ResourceType` / `Mode` / `DesiredState` / `ObservedState` / `Action` all have string as their underlying type, yet each is a distinct type. `DesiredState` and `ObservedState` in particular share the same strings, so their being separate types is the only thing preventing a mix-up.

### Consolidating the declarations per resource type

The facts that differ by type and carry no behaviour live in `model.TypeInfo`, in `core/model/resource_<type>.go`, with `resource.go` acting as the registry that lists them. Four facts are declared.

- The shape of the ARN
- The Tagging API filter
- The grammar of `ref`
- The tags that carry configuration meaning

The describe/stop/start behaviour does not live here. It requires AWS SDK clients, so it sits outside as `port.Target`. Handling of type-specific values likewise lives in the adapter that needs it.

### Where configuration transitions are implemented

The rules for `pin` / `unpin` / `schedule` / `disable` / `set-selector` live as methods on `model.GroupSpec`, and all but `disable` validate the result before returning. The application layer therefore cannot write a configuration that saves successfully but that the reconciler cannot follow.

`disable` alone does not validate, because it is the last resort for stopping a group whose configuration is broken and must not fail on account of that broken configuration.

### Where mocks live

Generated mocks live in a `mocks/` directory next to the package that declares the interface.

## Adding a supported resource type

Three places change, each in a different layer. The reconcile loop, the state layer, desired-state resolution, diagnosis, and notification know nothing of types and need no change. The places to change and what to change are given below.

| Where | What |
|---|---|
| Add `internal/core/model/resource_<type>.go` and one line to `typeInfos` in `resource.go` | Declare the type constant, the ARN `service` and resource-type, the regular expression a `ref` must satisfy, and the configuration items carried by tags. `KnownTypes`, selector validation, the discovery filter, and the frontends' enumeration and configuration display are all derived from this |
| Add a `port.Target` implementation under `internal/aws/compute` | Implement `Type()` / `Describe` / `Stop` / `Start`, declare the subset of the AWS client used as an interface, and add it to the `//go:generate` line in `compute.go`. `Start` receives the whole `model.Resource`, so type-specific start settings can be read from its tags |
| Add one line to `Targets` in `internal/wire` | The composition root is the only place that knows which AWS client backs which target |

A mismatch between the declaration and the wiring is caught by the tests in `internal/wire`. A break in the declaration itself (a missing `RefPattern`, a duplicated ARN pair, and so on) is caught by `TestTypeInfoDeclarations`.

Outside the code, the Describe and control APIs for the new type are added to the IAM policy.

## Container images

The reconciler and the web console are separate images. Both are based on `public.ecr.aws/lambda/provided:al2023` and carry a single static binary as `/var/runtime/bootstrap`. They are built from the same `Dockerfile` through `--target` and share the Go build stage.

They are kept separate because their lifecycles are independent. The web console is opt-in, and skipping it means pushing one image instead of two. A change to the console does not move the reconciler's image digest, so no unnecessary redeployment reaches the mandatory component.

Both images run on Lambda, but their routes to the runtime differ. The reconciler registers a Go handler directly, while the web console goes through the bundled Lambda Web Adapter. For details, see the integration summary in [on_lambda.md](on_lambda.md).

`cheapskate-cli` gets no image, since it does not run on Lambda. It is distributed as an archive per operating system.

## Dependencies

The module is named `cheapskate`. It is not meant to be imported as a library. The dependencies are the AWS SDK v2, aws-lambda-go (reconciler only), `adhocore/gronx` (cron evaluation), and testify and go.uber.org/mock for tests. The web console links no Lambda library at all.
