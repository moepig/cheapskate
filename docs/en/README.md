# cheapskate

A tool that manages the start/stop of RDS instances, Aurora clusters, ECS services, and EC2 instances. The desired state is held in DynamoDB, and a Lambda reconcile loop converges the actual state onto it.

It serves four purposes.

- Keeping RDS/Aurora stopped past the 7-day auto-start limit
- Cron-driven scheduled start/stop
- Dynamic resolution of the managed resources from an AWS tag selector
- Never acting on a resource whose desired state is `running`

RDS instances and Aurora clusters that AWS force-starts are stopped again from the start-completed events (`RDS-EVENT-0088` / `RDS-EVENT-0151`). Schedules and keep-stopped pinning are handled by the same reconcile loop and therefore cannot conflict. A target group is the unit of configuration; individual resources are never registered.

The control plane is one Lambda on a 5-minute interval plus one DynamoDB table.

Groups are configured and inspected through `cheapskate-cli`, a command-line tool that runs on a local machine — nothing has to be deployed to AWS for it. A web console offering the same operations from a browser can be deployed as well, at your option.

What is distributed is container images and binaries. Every release publishes two images, one for the reconciler and one for the optional web console, to `ghcr.io/moepig/cheapskate-reconciler` and `ghcr.io/moepig/cheapskate-webconsole`, along with the `cheapskate-cli` archives on the GitHub release. Lambda can pull images from ECR only, so an image reaches ECR either as a copy of the published one or as a build from source. No IaC template is distributed, and how the AWS resources are created is left open.

## Documentation

The documentation falls into three sets: Architecture, on how it works; Usage, on hosting it in an AWS account; and Development, on working on cheapskate itself. Each set is given below.

Architecture — how it works:

- [architecture/overview.md](architecture/overview.md) — overall composition, the data model, the reconcile loop
- [architecture/aws_resources.md](architecture/aws_resources.md) — AWS resource diagrams (reconcile loop / web console)
- [architecture/database.md](architecture/database.md) — the item structure of the DynamoDB table
- [architecture/trigger.md](architecture/trigger.md) — invocation paths and SNS notifications
- [architecture/logging.md](architecture/logging.md) — each component's log format and event list
- [architecture/metrics.md](architecture/metrics.md) — the division of labour between EMF and built-in Lambda metrics
- [architecture/cheapskate-cli.md](architecture/cheapskate-cli.md) — the design of the CLI
- [architecture/web_console.md](architecture/web_console.md) — the design of the web console
- [architecture/on_lambda.md](architecture/on_lambda.md) — running on Lambda: what RIE / LWA are and how each component uses them
- [architecture/emulation_local.md](architecture/emulation_local.md) — the local emulation setup

Usage — hosting in an AWS account:

- [usage/concepts.md](usage/concepts.md) — terms and the overall picture
- [usage/setup.md](usage/setup.md) — the specification of every resource to create (IAM policies, example commands)
- [usage/operations.md](usage/operations.md) — adding, changing, inspecting, and deleting the configuration records, and what they configure
- [usage/troubleshooting.md](usage/troubleshooting.md) — detecting failures, recovering from half-finished work, pruning leftovers with `doctor`
- [usage/config.md](usage/config.md) — the environment variable reference
- [usage/resource_tag.md](usage/resource_tag.md) — the tags applied to the AWS resources (selector / ECS scaling)

Development — working on cheapskate itself:

- [development/build.md](development/build.md) — building the binaries and the container images (reconciler / webconsole)
- [development/test.md](development/test.md) — unit and integration tests, and lint
- [development/mock.md](development/mock.md) — how mocks are generated and when each form is used
- [development/run_local.md](development/run_local.md) — running locally
- [development/github-actions.md](development/github-actions.md) — the CI, release, and Dependabot workflows
- [development/release.md](development/release.md) — cutting a release and dependency updates

## License

MIT
