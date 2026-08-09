# Releases and dependency updates

This document covers how a release is cut, what a release produces, and the reasoning behind the dependency-update settings.

goreleaser is never run locally. CI validates `.goreleaser.yaml` with `goreleaser check` on every pull request, and on a pull request touching the release path it also runs a snapshot build. The latter really does produce every cross-compilation target and both images, so it exercises the whole path while pushing nothing.

## Cutting a release

A release starts from pushing a tag.

```console
git tag v0.1.0
git push origin v0.1.0
```

The version is the tag itself, which goreleaser derives from it. For a semver prerelease (`v0.1.0-rc.1`), the GitHub release is created as a prerelease and the images' `latest` tags do not move. The whole test suite runs first, against the tagged commit, and if any of it fails no release is created for that tag.

A release produces the following.

| Artifact | Destination | Tags |
|---|---|---|
| The `cheapskate-reconciler` image | `ghcr.io/moepig/cheapskate-reconciler` | The version, `latest` |
| The `cheapskate-webconsole` image | `ghcr.io/moepig/cheapskate-webconsole` | The version, `latest` |
| The `cheapskate-cli` archives | The GitHub release | — |

Both images are multi-architecture (`linux/amd64`, `linux/arm64`). buildx assembles the binaries goreleaser cross-compiled, through `build/Dockerfile.reconciler` and `build/Dockerfile.webconsole`. What builds from source is the `Dockerfile` at the repository root, and that is the authoritative one for local builds and the image tests. For building locally, see the container images section in [build.md](build.md).

> [!IMPORTANT]
> The release Dockerfiles and the root `Dockerfile` have overlapping runtime stages. A change to the base image, an `ENV`, or the Lambda Web Adapter version has to be made in both.

Pushing to GHCR uses the workflow's `GITHUB_TOKEN` (`packages: write`). There is no secret to configure.

> [!IMPORTANT]
> After the first release, each image needs one manual step, once. A freshly created GHCR package is private regardless of the repository's visibility, and an unauthenticated `docker pull` fails against it, because a package inherits the linked repository's permissions rather than its visibility. Change each package to public from the repository's Packages (package settings → Change visibility).

Later releases push to the existing package, so the visibility is preserved.

## Dependency updates

Dependabot opens pull requests weekly (`.github/dependabot.yml`), covering Go modules, Actions, and the base images in the root and release Dockerfiles. Things that always move together (the AWS SDK modules, the Actions, the images) are grouped into one pull request each. All of them are checked by `ci.yml`, image tests included.

The Lambda Web Adapter is pinned by tag in the web console's Dockerfile and is covered by the docker updates. Unlike the Go dependencies it does not appear in `go.mod`, so without this nothing would notice its version moving.

### Days since release

Every ecosystem is configured with `cooldown.default-days: 7`, so Dependabot does not propose a version until a week after its publication. These updates are merged automatically, leaving no human between a malicious publication and `main`. A poisoned release is usually found and pulled within a few days, so keeping a week's distance costs little. Dependabot applies a 3-day cooldown even unconfigured; 7 days is that value deliberately extended.

> [!NOTE]
> Security updates are exempt from the cooldown. An update for a known CVE is proposed regardless of how recently it was published.

### Auto-merge

`dependabot-auto-merge.yml` runs on each of Dependabot's pull requests and calls `gh pr merge --auto` for patch and minor updates. Major updates are excluded and read by a human. The condition is an allowlist of two update types rather than "anything that is not major", so an update whose type could not be determined stops here too. On a grouped pull request the metadata reports the largest update type in the group, so ten patches with one major among them hold the whole group back.

The workflow itself merges nothing. `--auto` records the intent, and GitHub performs the merge once the required checks pass, which makes branch protection on `main` the real gate. The repository settings this depends on are given below.

- Enable Settings → General → Allow auto-merge. Disabled, `gh pr merge --auto` fails
- Require the `ci.yml` checks in branch protection (or a ruleset) on `main`. For which check names to select, see the check names section in [github-actions.md](github-actions.md)

> [!WARNING]
> With no required checks configured, a pull request is merged the moment `--auto` is set. The tests act as no gate at all, and dependency updates reach `main` unchecked.
