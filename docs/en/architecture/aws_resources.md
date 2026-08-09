# AWS resource diagrams

The AWS resources to create fall into two groups: the reconcile loop, which runs permanently, and the web console, which is deployed only if wanted. Beyond sharing the DynamoDB state table the two are independent, and the reconcile loop is complete without the web console.

## Resources for the reconcile loop

Drawing the resources that make up the reconcile loop, and the flow of data between them, gives the following diagram.

```mermaid
flowchart LR
    ops["cheapskate-cli / IaC /
    aws dynamodb put-item"]

    subgraph account["AWS account"]
        sched["EventBridge Scheduler
        rate(5 minutes)"]
        schedrole["execution role
        (scheduler.amazonaws.com)"]
        rule["EventBridge rule
        aws.rds events"]
        fn["Reconciler Lambda
        (container image,
        reserved concurrency 1)"]
        role["execution role
        (lambda.amazonaws.com)"]
        ddb[("DynamoDB state table
        group# / override# / status#")]
        tagapi["Resource Groups
        Tagging API
        (GetResources, read-only)"]
        rds["RDS instances /
        Aurora clusters"]
        ecs["ECS services"]
        aas["Application
        Auto Scaling"]
        ec2["EC2 instances"]
        sns["SNS topic
        (optional)"]
        logs["CloudWatch Logs"]
    end

    ops -- "writes group# / override#" --> ddb

    schedrole -. "lambda:InvokeFunction" .-> sched
    sched -- "Input: {}" --> fn
    rule -- "allowed by the resource-based
    policy (no role)" --> fn

    fn -. assumes .-> role
    role -- "Scan/GetItem/PutItem/UpdateItem" --> ddb
    role -- "GetResources" --> tagapi
    role -- "Describe/Stop/Start" --> rds
    role -- "DescribeServices/UpdateService" --> ecs
    role -- "DescribeScalableTargets/
    RegisterScalableTarget" --> aas
    role -- "DescribeInstances/
    Start/StopInstances" --> ec2
    role -- "Publish" --> sns
    role -- "CreateLogGroup/Stream,
    PutLogEvents" --> logs

    fn <--> ddb
    fn --> tagapi
    fn --> rds
    fn --> ecs
    ecs --- aas
    fn --> ec2
    fn --> sns
    fn --> logs
```

The role of each resource in the diagram, and whether it is required, is collected below.

| Resource | Role | Required |
|---|---|---|
| DynamoDB state table | The only persistent store, holding the desired state (`group#`/`override#`) and the results the reconciler writes (`status#`) | Required |
| Reconciler Lambda (container image) | The reconcile loop itself. Reserved concurrency of 1 rules out concurrent runs | Required |
| Lambda execution role | DynamoDB reads and writes, `tag:GetResources`, the Describe and control APIs for RDS/ECS/EC2, Application Auto Scaling, `sns:Publish` (when configured), and CloudWatch Logs | Required |
| EventBridge Scheduler (`rate(5 minutes)`) | The trigger for the periodic reconcile. Passes `{}` | Required |
| EventBridge rule (`aws.rds` events) | Triggers an immediate reconcile when an RDS auto-start event arrives. Target invocation is allowed by the Lambda's resource-based policy, so the rule needs no IAM role | Required |
| Resource Groups Tagging API | The only route from a selector to the resources | Required |
| RDS / ECS / EC2 (including Application Auto Scaling) | The resources being started and stopped. Permissions and connections for unmanaged resource types may be omitted | Only for the types in use |
| SNS topic | Where notifications of actions and failures go. With none configured, notification is a no-op | Optional |
| CloudWatch log group | Where the function's logs go. Creating it in advance and setting a retention period is recommended | Optional |

## Resources for the web console

Drawing the resources that make up the web console, and the route a browser takes to reach it, gives the following diagram.

```mermaid
flowchart LR
    user(["browser
    inside an allowed CIDR"])

    subgraph account["AWS account"]
        apigw["API Gateway REST API (v1)
        root + {proxy+} as ANY proxy integration"]
        policy["resource policy
        IP allowlist"]
        fn["Webconsole Lambda
        (webconsole image)"]
        role["execution role
        (lambda.amazonaws.com)"]
        ddb[("DynamoDB state table
        (shared with the reconcile loop)")]
        tagapi["Resource Groups
        Tagging API
        (GetResources, read-only)"]
        logs["CloudWatch Logs"]
    end

    user -- HTTPS --> apigw
    apigw -. applies .-> policy
    apigw -- "proxy event
    (turned into HTTP by the Lambda Web Adapter)" --> fn
    fn -. assumes .-> role
    role -- "Scan/GetItem/PutItem/DeleteItem" --> ddb
    role -- "GetResources" --> tagapi
    role -- "CreateLogGroup/Stream,
    PutLogEvents" --> logs
    fn <--> ddb
    fn --> tagapi
    fn --> logs
```

The role of each resource in the diagram is collected below.

| Resource | Role |
|---|---|
| API Gateway REST API (v1) | The only entrance from a browser. v1 is used because the HTTP API (v2) has no resource policy, which is what the IP restriction needs |
| Resource policy (IP allowlist) | The only access control |
| Webconsole Lambda | A separate function, built from a different container image than the reconciler |
| Lambda execution role | `dynamodb:Scan/GetItem/PutItem/DeleteItem` on the state table, the `Describe*` calls per resource type, `tag:GetResources`, and CloudWatch Logs. It holds no RDS/ECS/EC2 control permissions |
| DynamoDB state table | The same table as the reconcile loop. It writes `group#`/`override#`, and for `status#` it only reads and deletes orphans |
| Resource Groups Tagging API | Used to list the discovered resources on a group page |

Deploying it is optional. Running the console locally makes the AWS resources in this section unnecessary.

## The relationship between the two

The state table is the only shared resource, and the two never write to the same items. For details, see the read/write matrix in [database.md](database.md).

The reconciler Lambda and the webconsole Lambda are separate images, separate functions, and separate execution roles, and can be built and deployed independently. Without the web console, `cheapskate-cli` fills the same role. Only the access route differs; the shape of the items written is the same.
