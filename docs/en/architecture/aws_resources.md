# AWS resources and IAM

The reconciler requires the following access:

| Service | Access |
| --- | --- |
| DynamoDB | `Query` on the `CONFIG` partition only |
| Resource Groups Tagging API | `GetResources` |
| RDS | Describe, start, and stop DB instances and DB clusters |
| ECS | `DescribeServices` and `UpdateService` |
| Application Auto Scaling | Describe and register ECS scalable targets |
| EC2 | Describe, start, and stop instances |
| SNS | Optional `Publish` to the configured topic |
| CloudWatch Logs | Lambda log delivery |

The CLI and web console require `Query`, `GetItem`, `PutItem`, `UpdateItem`, and `DeleteItem` on the `CONFIG` partition. They also require `GetResources` and read-only Describe calls for `show` and group detail pages.

cheapskate does not create log groups, alarms, dashboards, or topics.
