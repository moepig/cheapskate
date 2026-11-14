# GitHub Actions

This document describes the workflows for CI, dependency auto-merge, release PR creation, and publication. Each job authenticates with GITHUB_TOKEN.

## Workflows

| File | Purpose |
| --- | --- |
| ci.yml | Tests, snapshot builds, and the ci aggregate check |
| test.yml | Reusable lint, unit, integration, image, and config checks |
| dependabot-auto-merge.yml | Metadata action and GitHub auto-merge |
| release-please.yml | Release Please PRs, tags, and draft releases |
| distribute.yml | GoReleaser, distribution, and publication |

CI runs on main pushes and pull requests. The ci check fails if a required job fails or is skipped. The reusable jobs appear as test / lint, test / unit, test / integration, test / image, and test / goreleaser. release-snapshot builds every target and image without publication. Integration and image tests use Docker and need no AWS credentials.

## Merge requirements

Require ci from GitHub Actions on main, an up-to-date branch, and reviews. Dismiss approvals after a new push. Enable DEPENDABOT_AUTO_MERGE only after these settings and CI have been verified. The metadata action verifies Dependabot commits; the workflow uses GitHub CLI to approve and reserve squash auto-merge for minor and patch updates. Major and unknown updates remain manual. PR code is never checked out by the privileged auto-merge workflow.

## Releases

Release Please runs on main pushes or manual dispatch. It creates a release PR when main contains unreleased fix or feat changes, updates an existing PR, and creates no new PR when there are no releasable changes. There is no scheduled run. After a dependency PR merges through GITHUB_TOKEN, run Release Please manually on main with tag left empty because that merge does not trigger a push workflow.

A maintainer reviews and merges the release PR. That merge pushes to main and automatically starts Release Please, which creates the tag and draft release and passes the merge SHA to distribute.yml. GoReleaser uploads archives and versioned containers. The workflow updates stable latest tags and publishes the draft.

Rerun a failed job after inspecting the release and registry state. Use a tag input only to retry an existing draft; that run skips PR creation. See the [release flow and operations](../../dependency-release.md#公開) for the creation and publication steps.

## Workflow changes

Pin external actions to complete commit SHAs with version comments for Dependabot. Local reusable workflows resolve at the caller commit. Keep the aggregate ci check dependent on every required job. Avoid workflow path filters that omit required results.
