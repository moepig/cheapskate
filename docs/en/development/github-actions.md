# GitHub Actions

Release Please creates the reviewed release. GoReleaser performs build and publication with GITHUB_TOKEN. Use immutable releases and investigate partial publication before issuing a new version. See the dependency and release operations document.

## Workflows

| File | Purpose |
| --- | --- |
| ci.yml | Tests, snapshot builds, and required ci check |
| test.yml | Lint, unit, integration, image, and config checks |
| dependabot-auto-merge.yml | Signed metadata, App approval, merge reservation |
| release-please.yml | Release PRs, draft, build, verification, publication |

CI runs on PRs and main pushes. The required ci check rejects failures and skipped required jobs. Integration and image tests use Docker without AWS credentials. External actions use complete commit SHAs; checkouts do not persist credentials.

## Release and merge boundaries

Require ci from GitHub Actions, latest main, reviews, stale approval dismissal, and Code Owner review. Only application dependency minor and patch updates qualify for auto-merge. Actions, Docker, major, unknown, and draft updates remain manual. The privileged workflow does not check out PR code.

See [release operations](../../dependency-release.md) for activation, stopping, and recovery. Keep ci dependent on every required job and avoid workflow path filters that omit required checks.
