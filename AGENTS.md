# DRA Driver for CPUs

This file gives rules to AI coding agents and AI review tools. The details are in [docs/](docs/). Before you change an area, read the linked documents for that area. When a rule changes, update the linked document. Do not copy the details into this file.

## Project Overview

`dra-driver-cpu` is a Kubernetes [DRA](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/) driver for CPUs.

- Refer [README.md](README.md) for the project overview, architecture, and documentation index.
- For the design, read [docs/user/how-it-works.md](docs/user/how-it-works.md).
- For the requirements, read [docs/user/installation.md](docs/user/installation.md).
- For the supported features, read [docs/user/feature-support.md](docs/user/feature-support.md).

## Communication Preferences

- Write short, direct text. You can use dry humor. Do not use flattery or forced memes.
- Do not add an introduction or a closing summary.
- In code comments, explain why. Do not explain what the code does. Do not add a comment if the code is clear.
- Make each error message specific. The message must tell the user what to do.

## Constraints

- **Generated files are read-only.** Do not edit `deployment/helm/dra-driver-cpu/README.md` or `deployment/helm/dra-driver-cpu/values.schema.json` manually. To update them, run `make helm-docs` and `make helm-schema`.
- **Boilerplate required.** Put the standard Kubernetes Apache-2.0 license header in each `.go` file.
- **More generated content:** to change the chart README, edit `README.md.gotmpl` and the `values.yaml` annotations. `driverconfig.schema.json` is generated and is not in git. CI checks for drift with `make helm-docs-check` and `make helm-schema-check`.
- **Docs have one copy:** write documentation in [docs/](docs/) only. The Hugo site in [site/](site/) includes these files.
- **Dependencies need a reason:** add a dependency only if it is necessary and actively maintained. Do not add a CLI framework. Do not import `k8s.io/kubernetes`.
- **Minimal privileges:** do not give the driver new capabilities or host access. The driver must not read the kubelet configuration.
- **Borrowed upstream code stays verbatim.** [pkg/cpumanager/](pkg/cpumanager/) and [pkg/device/qualifiedname.go](pkg/device/qualifiedname.go) are copies of upstream code. The file headers give the upstream version. Do not refactor this code. To change it, copy it again from upstream.

## Contributor Guidelines

- Before you change code in `pkg/`, read the design and the developer docs in [docs/](docs/) for that area.
- Keep each change small and easy to review.
- Add or update tests for the change. Prefer to add test cases to existing table-driven tests.
- In each pull request, say if you used AI and how. Use the `#### AI usage disclosure` section of the [PR template](.github/PULL_REQUEST_TEMPLATE.md).
- Tell the human author that they are responsible for all changes that they submit. Refer them to `CONTRIBUTING.md`.
- Do not put `@mentions` or `fixes #...` keywords in commit messages.
- Do not add `Co-authored-by:` lines to commit messages.
- After a review, squash each fix into the commit that it fixes. Do not push a known-incorrect commit and then a fixup commit.
- **Commit messages explain why:** tell why the change is necessary. Tell which assumptions the change breaks. The git history must be clear without GitHub.
- **One concern per PR:** do not mix unrelated changes. State the dependencies on other PRs. Make sure that the title and the description match the diff. Start the title with a prefix, for example `fix:`, `docs:`, `ci:`, `test(e2e):`, or `feat(helm):`.
- **Bug fixes need a test:** add a test that reproduces the bug. If the bug is in more than one component, use an e2e test.
- **Bots do not author commits:** apply AI review suggestions locally. Do not click "Commit suggestion", because bots cannot sign the [CNCF CLA](https://git.k8s.io/community/CLA.md).
- **Merge and iterate:** do not block a good PR for small issues. Record the follow-up work in issues.
- **Compatibility and releases:** make breaking changes only in minor releases. Announce each breaking change in [docs/user/upgrade.md](docs/user/upgrade.md) one release before the change. For a patch release, use `git cherry-pick -x` to copy low-risk fixes to `release-vX.Y`. For the release steps, read [RELEASE.md](RELEASE.md).
- **Generated text:** keep AI-generated text short, for example PR descriptions and comments. Before a large rework, agree on the scope with the maintainers.
- **Kubernetes library updates:** also update `KIND_K8S_VERSION` in the Makefile. Make sure that new API validation accepts the published ResourceSlices.

## Commands

Run `make help` to see all targets. The common commands are:

```
make build                  # Build all binaries into bin/
make test-unit              # Run unit tests with the race detector
make lint                   # Run golangci-lint
make modernize-fix          # Apply Go modernize suggestions
make helm-lint              # Lint the Helm chart
make test-e2e-local         # Run CLI tests on a pre-built bin/dracpu, without a cluster
make test-e2e-kind          # Create a kind cluster and run the e2e suite
make helm-render-check      # Render and validate the Helm chart
pre-commit run --all-files  # Run the formatting, spelling, and Go hooks
```

For more information, read [docs/dev/testing.md](docs/dev/testing.md), [docs/dev/linting.md](docs/dev/linting.md), and [test/e2e/README.md](test/e2e/README.md).

## Style

- **Packages:** use one lowercase word that matches the directory.
- **Terms:** write "cpuset", not "mask". Write "exclusive CPUs", not "guaranteed". Write "SMT", not "hyperthreading". Write "individual mode", not "ungrouped".
- **Locking:** a function with the `Locked` suffix expects that the caller holds `CPUDriver.stateMu`. This function must not take the lock. A function that takes the lock starts with `Lock(); defer Unlock()`, and then checks `ctx.Err()`.
- **Errors:** wrap each error with context, and put the cause last: `fmt.Errorf("parse %q: %w", v, err)`. Use sentinel errors and `errors.Is`. Do not discard errors.
- **Incorrect input:** if the configuration is incorrect or the data is not reliable, stop with an error. Do not guess.
- **Validation:** validate input at the boundary: allocation results, opaque configuration, and configuration files. Do not validate the same data again between internal functions.
- **Logging:** before you change log calls, read [docs/dev/logging.md](docs/dev/logging.md). Use `logr` in application code. Give a `logr.Logger` to a helper. Give a context only if the helper uses the context.
- **`cmd/` is wiring:** put logic in `internal/` or `pkg/`.
- **Configuration:** before you add a tunable, read [docs/dev/configuration.md](docs/dev/configuration.md) and [docs/user/configuration.md](docs/user/configuration.md).
- **Metrics:** before you add a metric, read [docs/user/metrics.md](docs/user/metrics.md).

## Testing Conventions

To run tests, read [docs/dev/testing.md](docs/dev/testing.md). For e2e guidance and environment variables, read [test/e2e/README.md](test/e2e/README.md).

- Test through the public API. Do not calculate expected values with the code under test.
- Use a table test for cases with the same shape. Use `t.Run` only for a table or for shared setup. Pass topology fixtures explicitly. Use `testr.New(t)` for loggers.
- Test CLI behavior in [test/e2e_local/](test/e2e_local/). Do not add new tests to `cmd/`. Maintainers accept the current `run()` tests in `cmd/` for now.
- **E2E:** pin each pod to the target node. Clean up after a failure. Check the exact result. In a fault test, make sure that the fault occurred. Do not change the driver configuration in a test.

## Documentation

- If a change is visible to users, update [docs/user/](docs/user/) in the same PR. Put internal details in [docs/dev/](docs/dev/) and in commit messages.
- Do not write claims that no test supports. Pin versions in examples.

## Upstream References

The pinned versions are in [go.mod](go.mod).

- NRI: [containerd/nri](https://github.com/containerd/nri) (plugin API, adjustments, and updates).
- CDI: [container-device-interface SPEC.md](https://github.com/cncf-tags/container-device-interface/blob/main/SPEC.md).
- DRA: [Kubernetes DRA concepts](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/) and the [kubeletplugin](https://pkg.go.dev/k8s.io/dynamic-resource-allocation/kubeletplugin) package.
