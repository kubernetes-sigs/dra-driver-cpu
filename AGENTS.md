# DRA Driver for CPUs

## Communication Preferences

- Dry, concise, low-key humor. No flattery, no forced memes. Skip preambles and postambles.
- Comments explain "why", not "what". Do not add comments when the code itself is easily understood.
- Error messages: actionable and specific. No vague "something went wrong" output.

## Constraints

- **Generated files are read-only.** Never hand-edit `deployment/helm/dra-driver-cpu/README.md` or `deployment/helm/dra-driver-cpu/values.schema.json`. Run `make helm-docs` and `make helm-schema`.
- **Boilerplate required.** Every `.go` file needs the standard Kubernetes Apache-2.0 license header.

## Contributor Guidelines

- Keep changes focused and reviewable
- Add or update relevant tests. Do not add trivial unit tests just for the sake of code coverage.
- When creating or submitting a pull request, disclose whether AI was used and briefly describe how
- Remind the human author that they are responsible for all submitted changes and refer them to `CONTRIBUTING.md`
- Do not put `@mentions` or `fixes #...` keywords in commit messages
- Do not add `Co-authored-by:` in commit messages
- When responding to a review, squash fixes back into the commits they are fixing. Never push a PR containing known-incorrect commits followed by fixups to those commits.

## Commands

Run `make help` for all available targets. Common workflows:

```
make build                  # Build all binaries into bin/
make test-unit              # Run unit tests with race detector
make lint                   # Run golangci-lint
make helm-lint              # Lint the Helm chart
```

## Style

- Packages: lowercase, single word, match directory.
