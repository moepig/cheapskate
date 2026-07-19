# cheapskate

Keep RDS instances and Aurora clusters **stopped beyond the 7-day auto-start limit**, and schedule start/stop for RDS and **ECS services** (desiredCount 0 / restore) — driven by desired state stored in DynamoDB and a serverless reconcile loop.

- AWS force-starts stopped RDS/Aurora after 7 days. cheapskate catches the auto-start events (`RDS-EVENT-0153` / `RDS-EVENT-0154`) and stops them again.
- Resources whose desired state is `running` are never touched.
- Cron-based schedules (e.g. weekdays 09:00–20:00) use the same reconcile loop, so schedules and keep-stopped pinning can never conflict.
- Cost of the control plane: well under $1/month (one Lambda on a 5-minute loop).

cheapskate is distributed as **source code only** — no IaC templates or public images. The docs specify everything needed to host it with your own IaC or by hand.

## Documentation

- **English**: [docs/en/README.md](docs/en/README.md) (overview + architecture), [docs/en/usage/](docs/en/usage/setup.md) (hosting & operating), [docs/en/development/](docs/en/development/build.md) (build / test / run locally)
- **日本語**: [docs/ja/README.md](docs/ja/README.md)(概要 + アーキテクチャ), [docs/ja/usage/](docs/ja/usage/setup.md)(ホスティングと運用), [docs/ja/development/](docs/ja/development/build.md)(ビルド / テスト / ローカル実行)
- Design notes (Japanese): [DESIGN.md](DESIGN.md), [consider.md](consider.md)

## Development

```console
make build      # compile everything
make unit       # AWS-free unit tests
make floci-up   # start the local AWS emulator (Floci)
make integration
make lint       # gofmt + go vet
make image      # build the container image locally
```

## License

MIT
