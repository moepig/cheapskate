# cheapskate

cheapskate keeps tagged RDS instances, Aurora clusters, ECS services, and EC2 instances in the running or stopped state selected by a schedule or an override.

Each resource belongs to one group through `cheapskate:group=<group>`. Group configuration is stored in DynamoDB. A five-minute full reconcile reads every group and every tagged resource before it calls any resource API.

The project ships three executables:

- `reconciler`: the Lambda full-reconcile handler
- `cheapskate-cli`: group configuration and inspection
- `webconsole`: an optional browser interface

Build and test commands are available through `make`. User and architecture documentation starts at [docs/en/README.md](docs/en/README.md) and [docs/ja/README.md](docs/ja/README.md).

This project is licensed under the MIT License.
