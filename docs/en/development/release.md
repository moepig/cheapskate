# Releases and dependency updates

This document covers how a release is cut, what a release produces, and the reasoning behind the dependency-update settings.

Run goreleaser in CI rather than locally. For configuration validation on every pull request and snapshot builds on every CI run, see [GitHub Actions](github-actions.md).

## Cutting a release

Release Please creates the version and changelog in a release PR. A maintainer approves its CI run, reviews the release contents, and merges it manually. Release control checks the tag against the PR merge SHA and calls the distribution workflow directly.

GoReleaser attaches archives to the existing draft without replacing Release Please notes. Containers are staged with versioned tags; latest advances only after every target has been verified. Prereleases do not advance latest. The controller publishes the GitHub Release after checking its assets and SHA-256 checksums.

See [dependency and release operations](../../dependency-release.md) for holds, recovery, and required settings.

## Release artifacts

A release produces the following.

| Artifact | Destination | Image tags |
|---|---|---|
| The `cheapskate-reconciler` image | `ghcr.io/moepig/cheapskate-reconciler` | The version, `latest` |
| The `cheapskate-webconsole` image | `ghcr.io/moepig/cheapskate-webconsole` | The version, `latest` |
| The `cheapskate-cli` archives | The GitHub release | — |

`latest` remains as a moving tag for trials and discovering the newest release; do not deploy it. Refer to a version tag for Lambda deployment, or to a digest where the exact same contents must be reproducible.

Both images are multi-architecture (`linux/amd64`, `linux/arm64`).

## Release image builds

buildx assembles release images from the binaries cross-compiled by goreleaser, using `build/Dockerfile.reconciler` and `build/Dockerfile.webconsole`.

Local builds and image tests build from source with the `Dockerfile` at the repository root. For the commands, see the container images section in [Building](build.md).

> [!IMPORTANT]
> The release Dockerfiles and the root `Dockerfile` have overlapping runtime stages. A change to the base image, an `ENV`, or the Lambda Web Adapter version has to be made in both.

## Initial GHCR publication

Pushing to GHCR uses the workflow's `GITHUB_TOKEN` (`packages: write`). There is no secret to configure.

> [!IMPORTANT]
> After the first release, manually change each GHCR package to public once. A freshly created GHCR package is private regardless of the repository's visibility, and an unauthenticated `docker pull` fails against it, because a package inherits the linked repository's permissions rather than its visibility. Change each package to public from the repository's Packages (package settings → Change visibility).

Later releases push to the existing package, so the visibility is preserved.

## Dependency updates

Dependabot opens pull requests weekly according to `.github/dependabot.yml`. Updates cover Go modules, Actions, and the base images in the root and release Dockerfiles.

AWS SDK modules, Actions, and images are each grouped into the units updated together. All updates are checked by `ci.yml`, including image tests.

The Dockerfile frontend, Go builder, Lambda base image, and Lambda Web Adapter are all pinned by tag and digest and covered by Docker updates. Unlike Go dependencies, they do not appear in `go.mod`, so Dependabot updates their tags and digests together.

### Days since release

Dependabot proposes versions 7 days after publication. Every ecosystem is configured with `cooldown.default-days: 7`, extending the default 3-day cooldown.

Patch and minor updates are merged without human review. The waiting period allows time for a malicious release to be detected or withdrawn before Dependabot proposes the update.

> [!NOTE]
> Security updates are exempt from the cooldown. An update for a known CVE is proposed regardless of how recently it was published.

### Auto-merge

Dependency control reads signed Dependabot commits and validates the current diff, CI results, and main protection. Minor and patch updates can receive bot approval and a squash auto-merge reservation. Major or unknown updates require a maintainer review of the current SHA.

The required checks are ci, dependency-review, and release-ready, provided by GitHub Actions. Missing protection leaves auto-merge disabled. Updates that affect the product use fix(deps); test and repository-only updates use chore(deps). No separate security evidence registration is used.
