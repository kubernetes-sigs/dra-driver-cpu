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

- `DRACPU_E2E_TARGET_NODE`: the CPU-related tests don't need any specific node.
  If this variable is set, it should be the `hostname` of a valid worker node in
  the cluster against which the tests run.
  If it is not set, the suit will pick a random node among the workers.
