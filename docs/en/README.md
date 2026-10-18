# cheapskate documentation

This index covers deployment, daily operation, configuration specifications, resource behavior, and development.

Choose a reading path for your task:

| Task | Reading path |
| --- | --- |
| First deployment | [Concepts](usage/concepts.md) → [Setup](usage/setup.md) |
| Daily operation | [Daily operations](usage/operations.md) → [CLI examples](usage/cli_records.md) |
| Resource configuration | [Resource index](usage/resources/README.md) → Type-specific settings |
| Troubleshooting | [Troubleshooting](usage/troubleshooting.md) → [Logs and metrics](usage/monitoring.md) |
| Reading the code | [Architecture](architecture/overview.md) → [Contracts](development/reconcile.md) → [API sequences](development/resource_sequence.md) |

## Getting started

Start with groups and schedules, then register your first group using the setup guide.

| Document | Scope |
| --- | --- |
| [Groups and schedules](usage/concepts.md) | Membership, schedules, and override precedence |
| [Setup](usage/setup.md) | AWS setup, first configuration, checks, periodic execution |
| [AWS resources and IAM](usage/aws_resources.md) | Deployment infrastructure inventory |
| [IAM permissions](usage/iam.md) | Execution roles, read/control, optional SNS and encryption |

## Daily operation

Choose a guide according to the task:

| Document | Scope |
| --- | --- |
| [Daily operations](usage/operations.md) | Inspect, schedule, temporary start/stop, ECS, end management |
| [CLI group configuration examples](usage/cli_records.md) | Commands and their saved DynamoDB configuration |
| [Terraform group configuration](usage/terraform_records.md) | Registering, changing, and removing groups through IaC |
| [Web console](usage/web_console.md) | Pages, forms, override expiration, and public access |
| [Logs, metrics, and notifications](usage/monitoring.md) | State, cycle results, errors, success notifications |
| [Troubleshooting](usage/troubleshooting.md) | Checks and remedies by symptom |

## Configuration and behavior references

Use these documents for valid values and how they affect resources:

| Document | Scope |
| --- | --- |
| [Resource configuration and shared settings](usage/resources/README.md) | Resource settings and shared specification/operation links |
| [Runtime configuration](usage/config.md) | Connection, time zone, monitoring, and HTTP variables |
| [Group configuration](usage/group_config.md) | Names, cron, overrides, ECS limits, DynamoDB attribute types |
| [Resource tags](usage/resource_tag.md) | Tag locations, defaults, and constraints |
| [CLI reference](usage/cli.md) | Arguments, formats, exit codes, and update constraints |
| [Start and stop behavior](usage/resource_flow.md) | Timing, eligibility, manual changes, errors, concurrent runs |
| [ECS startup, shutdown, and scaling](usage/ecs.md) | Counts, bounds, suspension, tag changes, partial failures |
| [ECS configuration and current-state examples](usage/ecs_examples.md) | Start, tag change, resume, lost response, and stop examples |

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
