# dracpu e2e local tests

This suite contains tests which verify the `dracpu` binary behavior
but do not require a cluster to work.
These tests are `e2e` in the sense that they test the binary at the user boundary:
configuration/flags as input, stdout/stderr as output.

The binary must be built before running the tests; the tests assume the binary
exists at its canonical location (`bin/dracpu`) relative to the repository root.

The tests must never trigger a build; if any required artifact is missing, the
tests (or the suite should the main binary be missing) hard fail.
