# cheapskate

A tool that manages the start/stop of RDS instances, Aurora clusters, ECS services, and EC2 instances. The desired state is held in DynamoDB, and a Lambda reconcile loop converges the actual state onto it.

Features:

- Keeping RDS/Aurora stopped past the 7-day auto-start limit
- Cron-driven scheduled start/stop
- Dynamic resolution of the managed resources from an AWS tag selector
- Never acting on a resource whose desired state is `running`

RDS instances and Aurora clusters that AWS force-starts are detected through the start-completed events (`RDS-EVENT-0088` / `RDS-EVENT-0151`) and stopped again. Schedules and keep-stopped pinning are handled by the same reconcile loop and therefore cannot conflict. A target group is the unit of configuration; individual resources are never registered.

Composition: the control plane is one Lambda on a 5-minute interval plus one DynamoDB table, which costs well under $1/month.

Distribution: each release publishes two images, one for the reconciler and one for the optional web console, to `ghcr.io/moepig/cheapskate-reconciler` and `ghcr.io/moepig/cheapskate-webconsole`. Lambda can pull images from ECR only, so an image reaches ECR either as a copy of the published one or as a local build. No IaC template is distributed; the AWS resources are created by hand or with whatever IaC is already in use.

## Documentation

Both sets cover the same ground: [architecture/](docs/en/architecture/overview.md) for how it works, [usage/](docs/en/usage/setup.md) for hosting and operating it, and [development/](docs/en/development/build.md) for building, testing, and running it locally.

- English: [docs/en/README.md](docs/en/README.md)
- 日本語: [docs/ja/README.md](docs/ja/README.md)

## Development

The common entry points are `make` targets.

```console
make build       # compile everything
make unit        # AWS-free unit tests
make integration # spins up the local AWS emulator (Floci) via testcontainers automatically
make lint        # gofmt + go vet
make image       # build both container images (reconciler, webconsole) locally
```

## License

MIT
