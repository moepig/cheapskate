# Releases and dependency updates

goreleaser is never run locally: on every pull request CI validates `.goreleaser.yaml` with `goreleaser check` and then builds a snapshot, which exercises the whole release path — every cross-compiled target and both images — without pushing anything. The workflows that run all of this are described in [github-actions.md](github-actions.md).

## Cutting a release

```console
git tag v0.1.0
git push origin v0.1.0
```

The tag is the version: goreleaser derives it, and a semver prerelease (`v0.1.0-rc.1`) marks the GitHub release as a prerelease and holds back the `latest` image tag. The full test suite runs first, on the tagged commit, and a failure anywhere in it leaves the tag without a release.

What a release produces:

| Artifact | Where | Tags |
|---|---|---|
| `cheapskate-reconciler` image | `ghcr.io/moepig/cheapskate-reconciler` | version, `latest` |
| `cheapskate-webconsole` image | `ghcr.io/moepig/cheapskate-webconsole` | version, `latest` |
| `cheapskate-cli` archives | GitHub release | — |

Both images are multi-arch (`linux/amd64`, `linux/arm64`). They are assembled by buildx from binaries goreleaser cross-compiles, using `build/Dockerfile.reconciler` and `build/Dockerfile.webconsole` — the root `Dockerfile` is the one that compiles from source, and it stays the source of truth for local builds and the image tests ([build.md](build.md)). The runtime stages are duplicated between the two, so a change to a base image, an `ENV`, or the Lambda Web Adapter version has to be made in both places.

Pushing to GHCR uses the workflow's `GITHUB_TOKEN` with `packages: write`; no secret needs to be configured.

One manual step is needed after the very first release, once per image. A newly created GHCR package is private regardless of the repository's visibility — packages inherit a linked repository's access permissions, but not its visibility — so an anonymous `docker pull` of it fails. Set each package to public under the repository's Packages page (package settings → Change visibility). Later releases push to the package that already exists and keep whatever visibility it has.

## Dependency updates

Dependabot opens weekly pull requests (`.github/dependabot.yml`) for Go modules, Actions, and the base images of the root and release Dockerfiles. Updates are grouped so that a set that always moves together — the AWS SDK modules, the Actions, the images — arrives as one pull request. Each of them is gated by `ci.yml`, image tests included.

The Lambda Web Adapter is pinned by tag in the web console Dockerfiles and is covered by the docker updates; unlike the Go dependencies it is not in `go.mod`, so nothing else would notice it moving.

### Release age

Every ecosystem sets `cooldown.default-days: 7`, so Dependabot will not propose a version until it has been public for a week. A compromised release is usually found and yanked within days, and a week of distance costs nothing here — these updates merge themselves, so no review stands between a bad publish and `main`. Dependabot applies a 3-day cooldown even when unconfigured; 7 is the deliberate version of that. Security updates are exempt from cooldown by design, so a known CVE is still proposed immediately.

### Auto-merge

`dependabot-auto-merge.yml` runs on every Dependabot pull request and, for patch and minor updates, calls `gh pr merge --auto`. Major updates are left alone: a human reads them. The condition is an allowlist of the two update types rather than "anything that is not major", so a bump whose type cannot be determined stops as well. For a grouped pull request the metadata reports the highest bump in the group, so a single major among ten patches holds the whole group back.

The workflow never merges anything itself. `--auto` records the intent and GitHub performs the merge once the required checks pass, which means the branch protection rule on `main` is what actually gates this. Two repository settings have to be in place, or auto-merge is either impossible or unguarded:

- Settings → General → Allow auto-merge, enabled. Without it `gh pr merge --auto` fails.
- A branch protection rule (or ruleset) on `main` requiring the `ci.yml` checks, selected by the names those checks carry ([github-actions.md](github-actions.md)). Without required checks, `--auto` merges the pull request the moment it is set, and the tests become decorative.
