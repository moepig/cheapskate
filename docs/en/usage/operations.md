# Operations

`cheapskate-cli` and the web console use the same validation and conditional-write service.

## CLI

The global options are `-table <name>` and `-output text|json`.

```console
cheapskate-cli list
cheapskate-cli show --group dev
cheapskate-cli schedule --group dev -start '0 8 * * 1-5' -stop '0 20 * * 1-5'
cheapskate-cli override --group dev running
cheapskate-cli override --group dev stopped -for 2h
cheapskate-cli override --group dev disabled
cheapskate-cli clear-override --group dev
cheapskate-cli remove --group dev
```

`schedule` creates a group when absent or changes only its two cron attributes. An indefinite `override` can also create a group and changes only override attributes. A timed override requires an existing schedule. `clear-override` also requires a schedule. `remove` conditionally deletes the complete group item.

Every change first performs a strongly consistent read and validates the full item. A conditional-write conflict is returned without an automatic retry. Changes to schedule and override attributes preserve each other; two writes to the same attribute use DynamoDB's last-applied value.

Exit codes are:

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Arguments, AWS, DynamoDB, or internal failure |
| `2` | Invalid stored group or conditional-write conflict |

Text `list` writes valid groups to stdout and invalid rows to stderr. JSON `list` always writes one complete object containing `groups` and `errors` to stdout. Either form exits with code 2 when an invalid row exists. JSON `show` emits a structured error object for invalid stored data and returns code 2.

## Web console

The web console provides group list and detail pages plus schedule and override forms. Detail pages show `cheapskate:group=<group>`, ECS configuration tags, and current read-only observations. Dates are displayed and parsed in `DEFAULT_TIMEZONE`. A write conflict returns HTTP 409.

## Safely ending management

To leave resources in a known state:

1. Set an indefinite `running` or `stopped` override.
2. Wait at least one configured reconciler Lambda timeout after the write completes.
3. Invoke the reconciler manually or wait for the next periodic invocation.
4. Use `show` to confirm that every resource is stable in the selected state.
5. Remove the membership tags from the resources.
6. Remove the group.

Do not change that group again during this sequence. Waiting lets invocations that read older configuration finish before management ends.
