# GitHub Actions

CI, dependency updates, and release publication use standard GitHub Actions and repository configuration. Every workflow uses GITHUB_TOKEN; there is no repository-owned control action.

## Workflows

| File | Purpose |
| --- | --- |
| ci.yml | Tests, snapshot builds, and the ci aggregate check |
| test.yml | Reusable lint, unit, integration, image, and config checks |
| dependabot-auto-merge.yml | Metadata action and GitHub auto-merge |
| release-please.yml | Release Please PRs, tags, and draft releases |
| distribute.yml | GoReleaser, checksum verification, and publication |

CI runs on main pushes and pull requests. The ci check fails if a required job fails or is skipped. The reusable jobs appear as test / lint, test / unit, test / integration, test / image, and test / goreleaser. release-snapshot builds every target and image without publication. Integration and image tests use Docker and need no AWS credentials.

## Merge requirements

Require ci from GitHub Actions on main, an up-to-date branch, and reviews. Dismiss approvals after a new push. Enable DEPENDABOT_AUTO_MERGE only after these settings and CI have been verified. The metadata action verifies Dependabot commits; the workflow uses GitHub CLI to approve and reserve squash auto-merge for minor and patch updates. Major and unknown updates remain manual. PR code is never checked out by the privileged auto-merge workflow.

## Releases

Release Please manages versions, changelogs, tags, and draft releases. A maintainer merges its PR. The action output passes the tag and merge SHA to distribute.yml. GoReleaser attaches archives and stages versioned containers. The workflow checks uploaded SHA-256 checksums, advances stable latest tags, and publishes the draft.

Recovery is manual: rerun a failed job or provide an existing draft tag to the Release Please workflow. No custom Check Runs, state issues, or image ownership service are used. See [release operations](../../dependency-release.md) for retry limitations and repository settings.

## Workflow changes

Pin external actions to complete commit SHAs with version comments for Dependabot. Local reusable workflows resolve at the caller commit. Keep the aggregate ci check dependent on every required job. Avoid workflow path filters that omit required results.
