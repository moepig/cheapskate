# CLI reference

This document defines cheapskate-cli commands, options, output formats, exit codes, and configuration-update constraints.

## Invocation and global options

Place global options before the command name:

```console
cheapskate-cli [-table TABLE] [-output text|json] <command> [arguments]
```

Global options are:

| Option | Default | Purpose |
| --- | --- | --- |
| -table | CHEAPSKATE_TABLE, then STATE_TABLE_NAME | DynamoDB table name |
| -output | text | text or json output |

AWS credentials and region use AWS SDK configuration. Cron interpretation uses DEFAULT_TIMEZONE, defaulting to UTC.

## Commands

Arguments, effects, and prerequisites are:

| Command | Arguments | Behavior and prerequisites |
| --- | --- | --- |
| list | None | List group configuration |
| show | --group NAME | Display group configuration, member tags, and current AWS state |
| schedule | --group NAME -start CRON -stop CRON | Create a group or update both cron attributes |
| override | --group NAME running, stopped, or disabled [-for DURATION] | Set override; indefinite overrides can create groups; timed overrides require an existing schedule |
| clear-override | --group NAME | Remove override and expiration; requires a schedule |
| remove | --group NAME | Delete the entire group item |

-for takes a positive duration such as 2h or 30m. Omitting it creates an indefinite override. The CLI does not create, tag, start, or stop AWS resources. Configuration changes take effect in later reconciler cycles.

For examples, see [Daily operations](operations.md) and [CLI group configuration examples](cli_records.md).

## Output formats

Text list writes valid groups to stdout and invalid groups to stderr. JSON list writes one object containing groups and errors to stdout when the list read succeeds. Failure of the read itself is written to stderr.

JSON show contains group and resources. Each resource's config comes from tags; live contains AWS observations; live_error contains an inspection failure. An invalid stored group produces a structured error object.

CLI expiration timestamps always use UTC RFC 3339 regardless of DEFAULT_TIMEZONE.

## Exit codes

Exit codes are:

| Code | Meaning |
| --- | --- |
| 0 | Success; show may still contain individual resource inspection errors |
| 1 | Arguments, proposed-configuration validation, AWS, DynamoDB, or internal failure |
| 2 | Invalid stored group or conditional-write conflict |

List returns 2 if any group is invalid. Show returns 0 even when individual resource inspection fails; inspect live_error as well as exit status.

## Updates and conflicts

Schedule, override, and clear-override validate the complete resulting group. Schedule preserves overrides and ECS limits. Override and clear-override preserve the schedule and ECS limit. Remove deletes a decodable item without validating cron expressions or cross-attribute constraints.

Conditional-write conflicts are not retried automatically. Read again and explicitly repeat the intended change. Changes to different attributes preserve one another. Changes to the same attributes are not detected as conflicts; the last value applied by DynamoDB remains.

> [!WARNING]
> Do not delete and recreate a group under the same name concurrently with another configuration change. Write conditions check attribute existence, so a stale request can change or delete the recreated group.
