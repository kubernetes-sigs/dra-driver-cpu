# E2E test suite

The E2E tests want to manage the CPU allocation on the tested nodes.
The CPU allocation state is ultimately shared node state. Thus, the test should run serially
because it's simpler to ensure correctness and reliable runs (avoid flakes).

## extending the E2E test suite

While considering extending the testsuite, adding tests, cases or sub-suites, please consider the following:

- The E2E tests are the closest, often identical, representation of user flows. The E2E test do have a very important
  signal and value, but they also are the costliest of the tests. To run them, we need to setup a full cluster.
  In general, prefer adding unit or integration tests, because they are cheaper, so they can run faster and more often.
- Related to the previous point, is very unlikely that a "right number" of E2E tests even exists.
  The project need to evaluate on a case-by-case manner, and the "right number" of E2E test may change over time.
- The single most important characteristic of a E2E test is its signal. Good E2E test should
  1. represent a real user flow and
  1. do it so reliably.
     Flakes should be avoided and actively mitigated. The experience strongly suggests that is better to have a small
     set of representative, high value, highly reliably tests than a larger set of flakier tests.

## configure using environment variables

The following environment variable allow to change the behavior of the e2e tests.
However, some Makefile targets also behave differently depending on the value of some variables;
this applies to Makefile targets which are meant to simplify or implement testing/CI flows.
In general, it is safe to assume that makefile targets beginning with `ci-` or `test-e2e-`
honor these settings, where applicable.

- `DRACPU_E2E_VERBOSE`: (optional) if set (any nonempty value; if unsure, use `1`),
  *the Makefile* will emit extra debug messages while executing targets.
  Please use the go logging configuration for the e2e tests proper.
  There's no support for verbosity levels.
- `DRACPU_E2E_TEST_IMAGE`: (mandatory) the full pullSpec of the test image the suite should
  use as container image to run test containers. The default CI configuration sets this
  value automatically.
- `DRACPU_E2E_TARGET_NODE`: (optional) the CPU-related tests don't need any specific node.
  If this variable is set, it should be the `hostname` of a valid worker node in
  the cluster against which the tests run.
  If it is not set, the suit will pick a random node among the workers.
- `DRACPU_E2E_RESERVED_CPUS`: (optional) if set, it is meant to mirror the driver `--reserved-cpus`
  setting. The value must be a linux cpuset string. The tests will ensure no containers
  consume these CPUs.
  NOTE: the Makefile defaults to "0" if not set, because a nonempty value is needed internally.
  NOTE: at the moment is not easy to autodetect this setting from the driver configuration.
  Future version may autodetect it.
- `DRACPU_E2E_CPU_DEVICE_MODE`: (optional): If set, change the behavior of the tests to assume
  the driver is configured with the corresponding device mode. Is meant to mirror the driver
  `--cpu-device-mode` setting.
  NOTE: the Makefile defaults to "grouped" if not set, because a nonempty value is needed internally.
  NOTE: Likewise the e2e tests, the Makefile also behaves differently depending on this setting.
  It is recommended to set it before to run targets like `make ci-manifests`.
