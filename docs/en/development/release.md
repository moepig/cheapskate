# Releases and dependency updates

This document covers how a release is cut, what a release produces, and the reasoning behind the dependency-update settings.

Run goreleaser in CI rather than locally. For configuration validation on every pull request and snapshot builds when the release path changes, see [GitHub Actions](github-actions.md).

## Cutting a release

A release starts from pushing a Git tag. The whole test suite runs against the tagged commit before publication; if any test fails, no release is created for that tag.

```console
git tag v0.1.0
git push origin v0.1.0
```

goreleaser derives the version from the Git tag. For a semver prerelease (`v0.1.0-rc.1`), the GitHub release is created as a prerelease and the images' `latest` tags do not move.

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

`dependabot-auto-merge.yml` runs on each of Dependabot's pull requests and calls `gh pr merge --auto` for patch and minor updates. These two update types form an allowlist. Major updates require human review. Updates whose type cannot be determined are also excluded from auto-merge.

For a grouped pull request, metadata reports the largest update type in the group. Ten patch updates with one major update therefore leave the whole group pending.

`--auto` records the auto-merge setting, and GitHub performs the merge once the required checks pass. Branch protection on `main` defines those required checks.

Auto-merge requires the following repository settings:

- Enable Settings → General → Allow auto-merge. Disabled, `gh pr merge --auto` fails
- Require the `ci.yml` checks in branch protection (or a ruleset) on `main`. For which check names to select, see the check names section in [GitHub Actions](github-actions.md)

> [!WARNING]
> With no required checks configured, a pull request is merged the moment `--auto` is set. Successful tests are not a merge requirement, and dependency updates reach `main` unchecked.
