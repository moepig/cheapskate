# Mocks

AWS SDK client interfaces use typed mocks generated with `go.uber.org/mock`. Regenerate them after an interface change:

```console
make generate
```

Generated files are committed. `internal/state/mocks/dynastore.go` is a hand-written in-memory DynamoDB implementation used behind the generated API mock. It supports the store's Query, GetItem, PutItem, UpdateItem, and DeleteItem operations, including conditional failures and Query pagination.

Application ports use stateful hand-written doubles in `internal/app/port/porttest`. They plant discovered resources and observations, record Start and Stop calls, and inject failures. This keeps application tests focused on domain values instead of AWS SDK request types.

Assertions use `testify`. A compile-time interface assertion should accompany every hand-written double.
