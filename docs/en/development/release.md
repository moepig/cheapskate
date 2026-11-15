# Releases and dependency updates

This document covers how a release is cut, what a release produces, and the reasoning behind the dependency-update settings.

Run goreleaser in CI rather than locally. For configuration validation on every pull request and snapshot builds on every CI run, see [GitHub Actions](github-actions.md).

## Cutting a release

Release Please uses a dedicated Automation App on main pushes to create and update the release PR. A maintainer reviews its version, compatibility, changelog, and CI, then merges it manually. That push creates a tag and draft. Read-only build jobs create the archives and OCI images; a separate publication job verifies digests, records the publication manifest, and uploads only missing items.

Release Please creates the reviewed release. GoReleaser performs build and publication with GITHUB_TOKEN. Use immutable releases and investigate partial publication before issuing a new version. See [dependency and release operations](../../dependency-release.md).

## Release artifacts

A release produces the following.

| Artifact | Destination | Image tags |
|---|---|---|
| The `cheapskate-reconciler` image | `ghcr.io/moepig/cheapskate-reconciler` | The version |
| The `cheapskate-webconsole` image | `ghcr.io/moepig/cheapskate-webconsole` | The version |
| The `cheapskate-cli` archives | The GitHub release | — |

GHCR latest is not updated automatically. Refer to a version tag for Lambda deployment, or to a digest where the exact same contents must be reproducible.

Both images are multi-architecture (`linux/amd64`, `linux/arm64`).

## Release image builds

buildx assembles release images from the binaries cross-compiled by goreleaser, using `build/Dockerfile.reconciler` and `build/Dockerfile.webconsole`.

Local builds and image tests build from source with the `Dockerfile` at the repository root. For the commands, see the container images section in [Building](build.md).

> [!IMPORTANT]
> The release Dockerfiles and the root `Dockerfile` have overlapping runtime stages. A change to the base image, an `ENV`, or the Lambda Web Adapter version has to be made in both.

## Initial GHCR publication

The publication job pushes to GHCR using GITHUB_TOKEN with packages: write. The Automation App key is required separately by PR automation and Release Please, and stays in the main-only automation Environment.

New GHCR packages are private by default. After the first release, change each package to public in its Settings to allow unauthenticated pulls. Repository visibility alone does not change package visibility. Follow [GitHub's package visibility documentation](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).

Later releases push to the existing package, so the visibility is preserved.

## Dependency updates

Dependabot opens pull requests weekly according to `.github/dependabot.yml`. Updates cover Go modules, Actions, and the base images in the root and release Dockerfiles.

AWS SDK modules, Actions, and images are each grouped into the units updated together. All updates are checked by `ci.yml`, including image tests.

The Dockerfile frontend, Go builder, Lambda base image, and Lambda Web Adapter are all pinned by tag and digest and covered by Docker updates. Unlike Go dependencies, they do not appear in `go.mod`, so Dependabot updates their tags and digests together.

### Days since release

Dependabot proposes versions 7 days after publication. Every ecosystem is configured with `cooldown.default-days: 7`, extending the default 3-day cooldown.

Patch and minor updates merge after the required CI and review conditions pass. The waiting period allows time for a malicious release to be detected or withdrawn before Dependabot proposes the update.

> [!NOTE]
> Security updates are exempt from the cooldown. An update for a known CVE is proposed regardless of how recently it was published.

### Auto-merge

The metadata action validates the Dependabot author and commit signatures. Only application dependency minor and patch updates qualify for App approval and squash auto-merge. Major, unknown, Actions, Docker, and draft updates remain manual. Use draft status to hold an update; breaking-change is a classification label.