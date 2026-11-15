# GitHub Actions

The workflows run CI, validate dependency updates, and publish reviewed releases. PR automation and Release Please use the repository's dedicated App. Builds use read-only GITHUB_TOKEN; publication has only the required write permissions.

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

A maintainer reviews and merges the App-created release PR. That push creates the tag and draft; read-only jobs build the distribution. The publication job validates artifact hashes and records the manifest before adding missing assets and image tags. Only the greatest stable SemVer advances Latest. Recovery accepts a release_id and verifies the original App PR and human approval.

See [release operations](../../dependency-release.md) for activation, stopping, and recovery. Keep ci dependent on every required job and avoid workflow path filters that omit required checks.
