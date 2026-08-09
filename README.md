# cheapskate

Keep RDS instances and Aurora clusters **stopped beyond the 7-day auto-start limit**, and schedule start/stop for RDS, **ECS services** (desiredCount 0 / restore), and **EC2 instances** — driven by desired state stored in DynamoDB and a serverless reconcile loop. Resources are never registered individually: a **target group** carries the schedule/pin/override config plus a selector (an AWS tag key/value + resource types), and membership is resolved live via the Resource Groups Tagging API.

- AWS force-starts stopped RDS/Aurora after 7 days. cheapskate catches the start-completed events (`RDS-EVENT-0088` / `RDS-EVENT-0151`) and stops them again.
- Resources whose desired state is `running` are never touched.
- Cron-based schedules (e.g. weekdays 09:00–20:00) use the same reconcile loop, so schedules and keep-stopped pinning can never conflict.
- Cost of the control plane: well under $1/month (one Lambda on a 5-minute loop).

Each release publishes the two Lambda container images to `ghcr.io/moepig/cheapskate-reconciler` and `ghcr.io/moepig/cheapskate-webconsole`. Lambda pulls images from ECR only, so hosting starts by copying one into your own account — or by building it from this repository. No IaC templates are distributed; the docs specify everything needed to create the resources with your own IaC or by hand.

## Documentation

- **English**: [docs/en/README.md](docs/en/README.md) (overview + architecture), [docs/en/usage/](docs/en/usage/setup.md) (hosting & operating), [docs/en/development/](docs/en/development/build.md) (build / test / run locally)
- **日本語**: [docs/ja/README.md](docs/ja/README.md)(概要 + アーキテクチャ), [docs/ja/usage/](docs/ja/usage/setup.md)(ホスティングと運用), [docs/ja/development/](docs/ja/development/build.md)(ビルド / テスト / ローカル実行)

## Development

```console
make build      # compile everything
make unit       # AWS-free unit tests
make integration # spins up the local AWS emulator (Floci) via testcontainers automatically
make lint       # gofmt + go vet
make image      # build both container images (reconciler, webconsole) locally
```

## License

MIT
