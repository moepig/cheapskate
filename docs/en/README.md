# cheapskate documentation

This index covers deployment, daily operation, configuration specifications, resource behavior, and development.

## Getting started

Start with groups and schedules, then register your first group using the setup guide.

| Document | Scope |
| --- | --- |
| [Groups and schedules](usage/concepts.md) | Membership, schedules, and override precedence |
| [Setup](usage/setup.md) | AWS preparation, first configuration, verification, and enabling periodic execution |
| [AWS resources and IAM](usage/aws_resources.md) | Infrastructure and execution-role permissions |

## Daily operation

Choose a guide according to the task:

| Document | Scope |
| --- | --- |
| [Daily operations](usage/operations.md) | Inspection, schedules, temporary starts/stops, ECS changes, and ending management |
| [CLI group configuration examples](usage/cli_records.md) | Commands and their saved DynamoDB configuration |
| [Terraform group configuration](usage/terraform_records.md) | Registering, changing, and removing groups through IaC |
| [Web console](usage/web_console.md) | Pages, forms, expiration input, and public access settings |
| [Logs, metrics, and notifications](usage/monitoring.md) | Current state, cycle results, error detection, and successful-action notifications |
| [Troubleshooting](usage/troubleshooting.md) | Checks and remedies by symptom |

## Configuration and behavior references

Use these documents for valid values and how they affect resources:

| Document | Scope |
| --- | --- |
| [Runtime configuration](usage/config.md) | Connections, time zones, monitoring, and HTTP environment settings |
| [Group configuration](usage/group_config.md) | Names, cron, overrides, ECS limits, and DynamoDB attribute types |
| [Resource tags](usage/resource_tag.md) | Tag locations, defaults, and constraints |
| [CLI reference](usage/cli.md) | Arguments, formats, exit codes, and update constraints |
| [Start and stop behavior](usage/resource_flow.md) | Timing, eligibility, manual changes, failures, and overlapping invocations |
| [ECS startup, shutdown, and scaling](usage/ecs.md) | Counts, bounds, suspension, tag changes, and failure examples |

## Development

For changes to code or runtime structure, use:

| Document | Scope |
| --- | --- |
| [Architecture](architecture/overview.md) | Components, data flow, and Lambda runtime |
| [Build](development/build.md) | Binaries, container images, and ECR publishing |
| [Run locally](development/run_local.md) | AWS emulator and development environment |
| [Tests](development/test.md) | Verification scope and commands |
| [Mocks](development/mock.md) | Test clients and generation |
| [Reconcile implementation contracts](development/reconcile.md) | Read boundaries, isolation, reapplication, and notifications |
| [Resource operation sequences](development/resource_sequence.md) | AWS API order and branches for reading the code |
| [GitHub Actions](development/github-actions.md) | CI and automated update configuration |
| [Release](development/release.md) | Release procedure and dependency updates |

## Language

For Japanese, see [日本語ドキュメント](../ja/README.md).
