---
id: release
title: Release
sidebar_position: 8
---

# Release

Use this guide to publish a tested commit as a versioned container image and GitHub release. The
[release workflow](../../.github/workflows/300-release.yaml) publishes artifacts; environment configuration, migrations,
rollout, and rollback belong to [deployment](deployment.md).

## Trigger and prerequisites

The `release` workflow runs only through `workflow_dispatch`, with one required string input, `version`. Select `master`
when dispatching it. A tag push or a merge does not trigger this workflow.

Before dispatching:

1. Wait for a successful, completed `push` run of
   [Unified CI](../../.github/workflows/100-unified-workflow.yaml) for the current `master` commit in this repository.
   Pull request and manually dispatched CI runs do not satisfy this check.
2. Choose an unused version such as `v1.2.3` or `v1.2.3-rc.1`: a `v` prefix and valid SemVer are required, build metadata
   such as `+build.7` is rejected, and the complete string must be at most 128 characters. Neither a Git tag nor a GitHub
   release may already exist for this version.
3. Configure repository variables `RELEASE_APP_CLIENT_ID` and `RELEASE_APP_NAME`, and secret `RELEASE_APP_PRIVATE_KEY`
   for the release GitHub App. Its installation must cover this repository and permit Contents write access.
   `RELEASE_APP_NAME` must match the App slug. These values are used in the publication job, after the image is pushed.
4. Ensure the workflow's `GITHUB_TOKEN` can publish the `go42` package to GHCR. The build job declares Packages write,
   Contents read, Actions read, Attestations write, and ID token write permissions; repository and organization settings
   must allow those operations.

The validation job requires the workflow definition and dispatch SHA to match the selected `master` commit. If `master`
advances before validation checks its head, validation fails; dispatch again after CI succeeds for the new head. Dispatches
for the same version share a concurrency group and do not cancel an active run.

## Publish a version

In the repository's GitHub Actions view, open the `release` workflow, choose **Run workflow**, select `master`, enter the
version, and run it. This writes a container image, attestations, a Git tag, and a published GitHub release.

The workflow runs three jobs in sequence:

| Job | Behavior and expected result |
| --- | --- |
| `validate-release` | Validates the version, source commit, and completed CI run; records the source SHA and CI link in its summary |
| `docker-build` | Checks out that SHA, builds and pushes both Linux architectures, validates the digest, and produces provenance and SBOM attestations |
| `publish-release` | Checks the release App slug, then creates the tag at the selected SHA and a non-draft release named `Release <version>` |

The GitHub release includes generated release notes plus the source SHA, immutable image reference, successful CI link,
and release workflow link. Versions containing a prerelease suffix produce GitHub prereleases and are not marked latest;
stable versions use GitHub's `legacy` latest-selection behavior.

The App token allows the `release.published` event to trigger optional downstream workflows. This checkout does not
provide a deployment workflow consuming that event. Publishing a release does not install or upgrade the Helm chart.

## Artifacts and identity

The [container build workflow](../../.github/workflows/140-docker-build.yaml) publishes
`ghcr.io/<repository-owner>/go42:<version>`. The service/package name is explicitly `go42`, even if the repository is
renamed. The exact published image is identified by `ghcr.io/<repository-owner>/go42@sha256:<digest>` in the release body;
use that digest when selecting an image for deployment. The workflow publishes the supplied version tag and does not
configure `latest`, major-version, or minor-version image aliases.

The release build targets `linux/amd64` and `linux/arm64`. The [Dockerfile](../../Dockerfile) compiles the application with
CGO disabled, using the Go version from [go.mod](../../go.mod), and embeds the source commit and requested release version.
The [application](../../cmd/app/main.go) reports these as `build_commit` and `build_tag`. The image includes the application,
static assets, API definitions, and migrations; running those migrations remains a deployment step.

The build generates `sbom-go42-<version>.spdx.json` from the published digest as an SPDX JSON workflow artifact and attaches
build provenance and SBOM attestations to that image identity through GitHub's attestation actions. The release workflow
does not package standalone application binaries or Helm charts as release assets.

[Unified CI](../../.github/workflows/100-unified-workflow.yaml) uses separate `ci-<run-id>-<run-attempt>` image tags.
A release rebuilds the tested source with the requested version; it does not promote the CI image digest. The release
workflow does not rerun the application test suites against its new image. CI also treats image security scans and load
tests as non-blocking, so a successful run does not establish that every advisory check passed.

[Local build tasks](../../Taskfile.yaml) serve development: `task build` produces `.build/app`, and `task image` requests
a multi-platform build tagged `ghcr.io/go42-dev/go42:dev` without a push or load option. Neither task publishes a release.

## Verify publication and investigate failures

After the workflow completes:

1. Confirm all three release jobs succeeded. Open the linked GitHub release and compare its source SHA with the commit
   shown by the linked successful Unified CI run. Confirm the tag resolves to that SHA and the prerelease status matches
   the requested version.
2. Open the image package in GHCR and check the version tag and manifest digest against the release's immutable image
   reference. Confirm the image contains both `linux/amd64` and `linux/arm64` manifests.
3. Inspect the build's provenance and SBOM attestation summaries and download the named SBOM from the workflow artifacts.
   Confirm their subject digest matches the release image. These establish the published artifact's identity and
   inventory; verify the deployed application separately using the [deployment guide](deployment.md).

The image is pushed before attestations, SBOM generation, and GitHub release creation. A later failure can therefore leave
an image or attestations without a GitHub release; the workflow has no automatic rollback. Inspect the failed step and
existing image, tag, and release before retrying. Validation rejects an existing Git tag or release, but does not check
whether the version's GHCR image tag already exists.

These instructions describe checked-in workflow behavior. App installation, repository permissions, published artifacts,
and environment promotion policy need verification in the target repository; they are not established by source review.
