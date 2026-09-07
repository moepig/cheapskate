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

Every change first performs a strongly consistent read and checks unknown attributes and attribute types. Schedule, override, and clear-override validate the complete resulting group. Remove deletes a decodable item without validating cron expressions or cross-attribute invariants. A schedule change is rejected while an expired override remains because the resulting expiry is in the past; clear or replace that override first.

A conditional-write conflict is returned without an automatic retry. Changes to schedule and override attributes preserve each other; two writes to the same attribute use DynamoDB's last-applied value.

Do not delete and recreate a group under the same name concurrently with another configuration change. Write conditions check attribute existence only, so a stale request can change or delete the recreated group.

Exit codes are:

| Code | Meaning |
| --- | --- |
| `0` | Success |
| `1` | Arguments, AWS, DynamoDB, or internal failure |
| `2` | Invalid stored group or conditional-write conflict |

Text `list` writes valid groups to stdout and invalid rows to stderr. When the list read succeeds, JSON list writes one complete object containing groups and errors to stdout. A failure of the read itself goes to stderr and returns exit code 1. Either form exits with code 2 when an invalid row exists. JSON `show` emits a structured error object for invalid stored data and returns code 2.

Validation failure of a proposed group change returns exit code 1.

Show returns exit code 0 even when individual resource Describe calls fail. JSON includes the cause in each affected resource’s live_error, so inspect observations rather than exit status alone before ending management. CLI expiry timestamps use UTC RFC 3339 regardless of DEFAULT_TIMEZONE.

## Web console

The web console provides group list and detail pages plus schedule and override forms. Detail pages show `cheapskate:group=<group>`, ECS configuration tags, and current read-only observations. Dates are displayed and parsed in `DEFAULT_TIMEZONE`. A write conflict returns HTTP 409. The detail form initializes Until to two hours after page rendering; clear it to set an indefinite override. The index override form creates indefinite overrides.

## Safely ending management

To leave resources in a known state:

1. Set an indefinite `running` or `stopped` override.
2. Wait at least one configured reconciler Lambda timeout after the write completes.
3. Invoke the reconciler manually or wait for the next periodic invocation.
4. Use `show` to confirm that every resource is stable in the selected state.
5. Remove the membership tags from the resources.
6. Remove the group.

Do not change that group again during this sequence. Waiting lets invocations that read older configuration finish before management ends.
