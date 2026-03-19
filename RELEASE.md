# Release Process

This document outlines the process for creating a new official release for the DRA CPU Driver. The repository uses an automated release pipeline to handle branching and tagging.

## 1. Propose the Release

- Create a new GitHub Issue titled "Release vX.Y.Z" to track the release process.
- In the issue, gather a preliminary changelog by reviewing commits since the last release.
  We will add automation to streamline this process, and document how to use in this document. If the automation is unavailable, or if we need a manually curated list,
  a good starting point is `git log --oneline <last-tag>..HEAD`.

## 2. Trigger the Release Automation

Instead of manually creating branches and tags, the release is triggered by a pull request:

- The Release Shepherd creates a new PR targeting the `main` branch (for minor/major releases) or a `release-*` branch (for patch releases).
- In this PR:
  - Update the `VERSION` file at the repository root to the new semantic version (e.g., `v0.2.0`).
  - Update any documentation (like `docs/book/src/releases.md`), examples, or manifests as needed for the release.
- Ensure all tests are passing.
- Once the PR is merged, the Release Automation GitHub Action will trigger.

The automation will:

1. Validate the semantic version format.
1. Create and push a git tag `vX.Y.Z` at the merged commit.
1. If it is a minor or major release (e.g. `v0.2.0`), it will automatically branch off `release-vX.Y.Z` and push it to the repository.

## 3. Promote the Images

After the release tag is pushed, the images must be built, pushed to staging, and then promoted to the official registry.

### A. Verify Staging Images

- Monitor the image push job status on [Testgrid (sig-node-image-pushes)](https://testgrid.k8s.io/sig-node-image-pushes#post-dra-driver-cpu-push-images).
- The Prow job configuration is in [test-infra](https://github.com/kubernetes/test-infra/blob/master/config/jobs/image-pushing/k8s-staging-dra-driver-cpu.yaml) if troubleshooting is needed.
- Verify the images exist in the staging repository:
  ```sh
  skopeo list-tags docker://us-central1-docker.pkg.dev/k8s-staging-images/dra-driver-cpu/dra-driver-cpu | grep vX.Y.Z
  ```

### B. Create PR for Promotion

- Identify the image digests:
  ```sh
  skopeo inspect docker://us-central1-docker.pkg.dev/k8s-staging-images/dra-driver-cpu/dra-driver-cpu:vX.Y.Z --format '{{.Digest}}'
  ```
- Fork [kubernetes/k8s.io](https://github.com/kubernetes/k8s.io).
- Update
  `registry.k8s.io/images/k8s-staging-dra-driver-cpu/images.yaml`
  with the new digests and tags ([sample PR](https://github.com/kubernetes/k8s.io/pull/9176)).
- Submit the PR to `kubernetes/k8s.io`. Once it is approved and merged
  automation will schedule the promotion.

## 4. Final Verification and GitHub Release

Before publishing the release, verify the images are available at k8s-registry:

### A. Verify Official Registry

- Ensure the images are available at `registry.k8s.io`:
  ```sh
  skopeo list-tags docker://registry.k8s.io/dra-driver-cpu/dra-driver-cpu | grep vX.Y.Z
  ```

### B. Test Release Artifacts

- Checkout the release tag locally
  ```sh
  git show vX.Y.Z -q  # to verify the right tag and commit
  git checkout vX.Y.Z
  ```
- Generate the release manifests:
  ```sh
  rm -rf ./dist
  REGISTRY=registry.k8s.io TAG=vX.Y.Z make manifests OVERRIDE_IMAGE=true
  ls ./dist   # to confirm the generated artifacts
  ```
- Run a local E2E test in Kind (see references in `README.md`) using the generated manifests from the `dist/` directory to ensure they pull the correct images and function as expected.

### C. Create the GitHub Release

- Go to the [Releases page](https://github.com/kubernetes-sigs/dra-driver-cpu/releases) on GitHub.
- Find the new tag and click "Edit tag" (or "Draft a new release" and select the tag).
- Paste the final changelog into the release description.
- Upload the generated manifests (`dist/install.yaml`) as release artifacts.
- Publish the release.

## 5. Post-Release Tasks

- Close the release tracking issue.
- Announce the release on the `sig-node` mailing list. The subject should be: `[ANNOUNCE] DRA CPU Driver vX.Y.Z is released`.
