# Releases and dependency updates

This document covers how a release is cut, what a release produces, and the reasoning behind the dependency-update settings.

Run goreleaser in CI rather than locally. For configuration validation on every pull request and snapshot builds on every CI run, see [GitHub Actions](github-actions.md).

## Cutting a release

Release Please creates the version and changelog in a release PR. A maintainer approves its CI run, reviews the release contents, and merges it manually. The release workflow checks the tag against the PR merge SHA and calls the distribution workflow directly.

GoReleaser attaches archives to the existing draft without replacing Release Please notes. Containers are staged with versioned tags; latest advances only after every target has been verified. Prereleases do not advance latest. The workflow publishes the GitHub Release after checking its assets and SHA-256 checksums.

See [dependency and release operations](../../dependency-release.md) for stopping publication, manual retry, and required settings.

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

dependabot/fetch-metadata verifies Dependabot commits and reports the update type. The workflow uses GitHub CLI to approve and reserve squash auto-merge for minor and patch updates. Major and unknown updates require manual review.

Require the ci check from GitHub Actions, reviews, an up-to-date main base, and stale approval dismissal before enabling DEPENDABOT_AUTO_MERGE. Application dependencies use fix commits and Actions use chore commits through Dependabot configuration. There is no per-dependency product-impact analysis or security evidence registration.

Retry failed distribution jobs only after verifying the tag, draft, and registry state. An existing draft tag can be supplied to Release Please for a manual retry; versioned images may be replaced. No automatic recovery service or image ownership inspection is used.
