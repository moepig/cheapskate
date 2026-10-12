# cheapskate

cheapskate maintains scheduled running and stopped states for tagged RDS instances, Aurora clusters, ECS services, and EC2 instances. Resources sharing a group use the same schedule. Overrides keep them running, stopped, or temporarily outside automatic control.

Group configuration is stored in DynamoDB. Resource tags define membership and ECS startup settings. A Lambda reconciler normally applies configuration every five minutes. Administration is available through cheapskate-cli and an optional web console.

## Documentation

The guides cover deployment, daily operations, settings, and resource behavior:

- [English documentation](docs/en/README.md)
- [日本語ドキュメント](docs/ja/README.md)

For a first deployment, start with [Groups and schedules](docs/en/usage/concepts.md), then follow [Setup](docs/en/usage/setup.md). For contribution and local execution, see the development section of either documentation index.

## License

This project is licensed under the MIT License.
