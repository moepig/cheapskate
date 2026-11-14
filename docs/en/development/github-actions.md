# GitHub Actions

CI, dependency control, and releases use the workflows under .github/workflows. Authentication uses each job's GITHUB_TOKEN.

## Workflows

| File | Purpose |
| --- | --- |
| ci.yml | Tests, snapshot builds, and the ci aggregate check |
| test.yml | Reusable lint, unit, integration, image, and config checks |
| dependency-control.yml | Dependency policy and merge reservations |
| release-control.yml | Release PRs, recovery, and publication |
| distribute.yml | Archives and versioned container images |

CI runs on main pushes and pull requests. The aggregate ci job fails if a required job fails or is skipped. The reusable test jobs appear as test / lint, test / unit, test / integration, test / image, and test / goreleaser. The separate release-snapshot job builds every target and image without publication on every CI run. Integration and image tests use the runner's Docker daemon and need no AWS credentials.

## Required checks

Require ci, dependency-review, and release-ready from GitHub Actions on main. Also require an up-to-date branch and reviews, and dismiss stale approvals. The dependency controller verifies these settings before reserving auto-merge. Its decision uses the latest CI attempt for the current PR head.

## Trusted control

Dependency control runs from pull_request_target, workflow_run, scheduled, or manual events. It reads PR files through the API and does not check out PR code. The shared action is pinned to a full commit SHA. Reviews of manually merged dependency updates are reevaluated on the next scheduled or manual run.

Release control calls distribute.yml directly with the merged release PR SHA. GoReleaser keeps the existing draft and notes; container publication stages versioned tags before updating latest. Publication checks run before the draft is made public. See [release operations](../../dependency-release.md) for CI approval, holds, and recovery.

Each workflow defaults to contents: read. Control jobs add repository write permissions for their API operations. Distribution receives contents: write and packages: write. No additional secret is required.

## Workflow changes

Pin external actions to complete commit SHAs and retain their version comments for Dependabot. Local reusable workflows resolve at the caller commit. Keep ci job names and the configured ciJobs list aligned. Required workflows must not use path filters that leave PRs without a result.
