# Web console architecture

The web console is a frontend for performing the same operations as `cheapskate-cli` from a browser. Deploying it is optional, and the reconcile loop is complete without it.

## Technology stack

It is built from Go's `net/http` and `html/template` alone, with the templates embedded in the binary through `embed`. There is no JavaScript, no external asset, and no frontend build: every page is server-rendered HTML with form POSTs.

Whatever it runs on, it is one HTTP server with no Lambda-specific code. Nothing detects or branches on the environment, and the server started locally is the same one that runs on Lambda.

The routes exposed are given below.

| Route | Purpose |
|---|---|
| `GET /` | The list page |
| `GET /group` | The group page |
| `GET /doctor` | The diagnostics page |
| `POST /op` | Configuration operations |
| `POST /doctor` | Pruning orphaned records |

## Hosting

A container image separate from the reconciler is deployed as a separate Lambda function. The bundled Lambda Web Adapter converts between invocation events and HTTP. The console itself links no Lambda library and assumes the adapter in two respects only: the port it listens on, and how it obtains the client IP. For details, see the adapter's observable behaviour and its integration into webconsole in [on_lambda.md](on_lambda.md).

In front of it sits an API Gateway REST API (v1), because the HTTP API (v2) has no resource policy, which is what the IP restriction needs. The `BASE_PATH` environment variable is the path prefix as the browser sees it (the API Gateway stage name) and is used when generating links and redirects; the path in a proxy event does not include the stage.

## Access control

There is no authentication, and access control rests entirely on the IP allowlist in front. How each concern is handled is given below.

| Concern | Handling |
|---|---|
| Authentication | None. Access control is the IP allowlist in the API Gateway resource policy alone, so everyone inside an allowed CIDR can operate the console |
| CSRF | `POST` validates the `Origin` and `Sec-Fetch-Site` headers and rejects anything other than same-origin |
| CSP | `default-src 'none'`, `frame-ancestors 'none'`, and so on |
| Permissions | The execution role holds only `dynamodb:Scan/GetItem/PutItem/DeleteItem` on the state table, the `Describe*` calls per resource type, and `tag:GetResources` |

Every error shown on screen is also written to the log. With no authentication and an IP allowlist as the only access control, the log is the only place a history of changes can be traced. For details, see the web console event list in [logging.md](logging.md).

## Pages

The content of each page, and whether it performs discovery, is collected below.

| Page | Content | Discovery |
|---|---|---|
| List | Every group on one row (configuration + selector + override + last error) | No. Rendered from a single Scan of the whole table |
| Group | Configuration + override + the discovered resources (type / name / last action / observed state / when the transition started / last error / current state), plus the configuration forms | Yes |
| Diagnostics | The same diagnosis as `cheapskate-cli doctor`, and orphan pruning under the same conditions | Yes |

A discovery failure does not produce a 500; the error is shown within the page.

Orphan pruning does not act on the screen already rendered: it re-runs the diagnosis at the moment it is invoked. That keeps a record that stopped being orphaned since the page was opened from being deleted on the strength of a stale screen.
