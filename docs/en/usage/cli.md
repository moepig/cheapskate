# CLI reference

This document defines cheapskate-cli commands, options, output formats, exit codes, and configuration-update constraints.

## Invocation and global options

Place global options before the command name:

```console
cheapskate-cli [-config PATH] [-context NAME] [-table TABLE] [-output text|json] <command> [arguments]
```

Global options are:

| Option | Short option | Default | Purpose |
| --- | --- | --- | --- |
| -config | None | CHEAPSKATE_CONFIG, then the OS-specific default path | CLI config file path |
| -context | -c | Saved current context | Context for this invocation only |
| -table | None | Selected context, then environment variables | DynamoDB table name |
| -output | -o | text | text or json output |

The selected context supplies AWS profile and region when configured. Unset values use AWS SDK configuration.

The selected context also supplies the schedule time zone. If unset, it uses DEFAULT_TIMEZONE, which defaults to UTC.

Context settings take precedence over environment variables; -table overrides the context's table. Without a context table, the table name comes from CHEAPSKATE_TABLE, then STATE_TABLE_NAME. -context applies only to this invocation and does not change the saved selection.

## Contexts

A context is a named set of connection settings. Contexts and the current selection are saved as JSON and reused on later invocations.

Config commands operate locally without requiring AWS credentials or a table in the environment.

### Registering and selecting contexts

The context commands are:

| Command | Arguments | Behavior |
| --- | --- | --- |
| config set-context | NAME [options] | Create or update a context |
| config use-context | NAME | Save the current selection; the context must exist |
| config get-contexts | None | List by name; mark current selection with * |
| config current-context | None | Display the saved selection; fail if none is selected |

New contexts require --table. Optional settings are the AWS shared config profile name, AWS region, and IANA schedule time zone. Omitted fields retain their saved values; an empty optional value clears that setting.

set-context does not change the current selection. Use use-context to save the selection.

Credentials remain in AWS SDK configuration; the CLI saves the profile name.

The set-context options are:

| Option | Setting |
| --- | --- |
| --table TABLE | DynamoDB table name |
| --profile PROFILE | AWS shared configuration profile |
| --region REGION | AWS region |
| --timezone ZONE | IANA schedule time zone |

Use the same schedule time zone as the reconciler and web console.

To register and select a context, then temporarily use another context:

```console
cheapskate-cli config set-context dev --table cheapskate-dev --profile dev --region ap-northeast-1 --timezone Asia/Tokyo
cheapskate-cli config set-context prod --table cheapskate-prod --profile prod --region ap-northeast-1 --timezone Asia/Tokyo
cheapskate-cli config use-context dev
cheapskate-cli list
cheapskate-cli -context prod list
cheapskate-cli config current-context
```

The final command prints dev because -context applies only to that invocation.

### Configuration file

The default file locations are:

| OS | Path |
| --- | --- |
| Linux, XDG_CONFIG_HOME set | $XDG_CONFIG_HOME/cheapskate/config.json |
| Linux, XDG_CONFIG_HOME unset | ~/.config/cheapskate/config.json |
| macOS | ~/Library/Application Support/cheapskate/config.json |
| Windows | %AppData%\cheapskate\config.json |

-config overrides CHEAPSKATE_CONFIG.

A missing file means no saved contexts; it is created when saving a context or selection. Invalid files produce an error.

The file contains current_context and a contexts object keyed by name; context fields are table, profile, region, and timezone.

### JSON output from context commands

With -output json, the context commands return:

| Command | Result |
| --- | --- |
| get-contexts | CLI configuration object |
| set-context | Context name in name and saved context fields |
| use-context | Object containing current_context |
| current-context | Object containing current_context |

## Commands

All commands that accept --group also accept -g.

To display the app group using the dev context and JSON output:

```console
cheapskate-cli -c dev -o json show -g app
```

Arguments, effects, and prerequisites are:

| Command | Arguments | Behavior and prerequisites |
| --- | --- | --- |
| list | None | List group configuration |
| show | --group NAME | Display group settings, member tags, and AWS state |
| schedule | --group NAME -start CRON -stop CRON | Create a group or update both cron attributes |
| override | --group NAME running, stopped, or disabled [-for DURATION] | Set override |
| clear-override | --group NAME | Remove override and expiration; requires a schedule |
| ecs-max-count | --group NAME COUNT | Register or change ecs_max_count on an existing group |
| clear-ecs-max-count | --group NAME | Remove the ecs_max_count attribute from an existing group |
| remove | --group NAME | Delete the entire group item |

Indefinite overrides can create groups. Timed overrides require an existing schedule.

-for takes a positive duration such as 2h or 30m. Omitting it creates an indefinite override.

The CLI does not create, tag, start, or stop AWS resources. Configuration changes take effect in later reconciler cycles.

COUNT for ecs-max-count is an integer from 1 through 2147483647. The command does not create groups; create one first with schedule or an indefinite override.

The clear-ecs-max-count command removes the attribute instead of storing 0 and does not stop current resources. ECS startup and running configuration updates require registering a limit again.

All DynamoDB group configuration fields can be registered through the CLI.

For examples, see [Daily operations](operations.md) and [CLI group configuration examples](cli_records.md).

## Output formats

Text list writes valid groups to stdout and invalid groups to stderr.

JSON list writes one object containing groups and errors to stdout when the list read succeeds. Failure of the read itself is written to stderr.

JSON show contains group for saved group configuration and resources for the member resource array. Fields are:

| Field | Content |
| --- | --- |
| group | Saved group configuration |
| resources[].type | cheapskate resource type name |
| resources[].ref | Resource-specific identifier |
| resources[].arn | Resource ARN |
| resources[].config | Settings read from tags |
| resources[].live.state | State classified from AWS observations |
| resources[].live.detail | AWS status or ECS counts and scaling settings |
| resources[].live_error | State inspection failure |

An invalid stored group produces a structured error object.

CLI override expiration timestamps always use UTC RFC 3339 regardless of the schedule time zone.

## Exit codes

Exit codes are:

| Code | Meaning |
| --- | --- |
| 0 | Success; see show inspection errors below |
| 1 | Command or service failure; see details below |
| 2 | Invalid stored group or conditional-write conflict |

Code 1 covers invalid arguments, proposed-configuration validation, AWS, DynamoDB, and internal failures.

List returns 2 if any group is invalid.

Show returns 0 even when individual resource inspection fails; inspect live_error as well as exit status.

## Updates and conflicts

Schedule, override, clear-override, ecs-max-count, and clear-ecs-max-count validate the complete resulting group. They preserve these attributes:

| Command | Preserved attributes |
| --- | --- |
| schedule | Override and ECS limit |
| override, clear-override | Schedule and ECS limit |
| ecs-max-count, clear-ecs-max-count | Schedule and override |

Remove deletes a decodable item without validating cron expressions or cross-attribute constraints.

Conditional-write conflicts are not retried automatically. Read again and explicitly repeat the intended change.

Changes to different attributes preserve one another. Changes to the same attributes are not detected as conflicts; the last value applied by DynamoDB remains.

> [!WARNING]
> Do not delete and recreate a group under the same name concurrently with another configuration change. Write conditions check attribute existence, so a stale request can change or delete the recreated group.
