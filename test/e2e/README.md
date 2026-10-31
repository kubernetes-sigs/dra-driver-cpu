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

- `DRACPU_E2E_LABEL_FILTER`: (optional) a [Ginkgo label-filter expression](https://onsi.github.io/ginkgo/#spec-labels)
  used by the Makefile `test-e2e` target to select which specs to run. For example,
  set it to `metrics` to run only metrics specs, or `!negative` to exclude specs
  labeled `negative`.

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

- `DRACPU_E2E_ALLOCATOR`: (optional): If set, change the behavior of the tests to assume
  the driver is configured with the corresponding allocator. Is meant to enable external allocator testing
  and/or machine mode testing.
  NOTE: the Makefile defaults to "cpumanager" if not set, because a nonempty value is needed internally.

- `DRACPU_E2E_NODE_ALLOCATABLE_MAPPING`: (optional, default `false`): when `true`, *the Makefile*
  `ci-kind-setup` target deploys the driver with the
  `driverConfig.publishNodeAllocatableResourceMapping: true` option; any other value
  deploys without it. The cluster must have the `DRANodeAllocatableResources` feature
  gate; the stock kind configs enable it (they require a 1.37+ node image, which is the
  Makefile default).

- `DRACPU_E2E_DUMP_RAW_LOGS`: (optional): if set to any value which is true-ish (e.g. `1`, `true`...)
  makes the tests which verify the contextual logging integrity dump the full raw captured logs
  before to run any actual test. Useful for troubleshooting and test fixing/tuning.
  NOTE: setting this value will make the test output significantly larger.

- `DRACPU_E2E_DAEMONSET_NAMESPACE`: (optional, default `kube-system`): the namespace where the
  dra-driver-cpu DaemonSet and its pods are deployed. Override this when the driver is installed
  in a namespace other than `kube-system`.

- `DRACPU_E2E_NODE_RUNTIME`: (optional, default `docker`): the container runtime CLI the
  node fault injection specs use to restart a node or its kubelet. Set it when the nodes of
  the cluster under test are not managed by `docker`.

## node fault injection

> **Warning:** these specs reboot a worker node and restart its kubelet. Run them only
> against a cluster you can throw away. Exclude them with
> `DRACPU_E2E_LABEL_FILTER='!node-fault'` when running against a cluster you care about.

The specs labeled `node-fault` cover the reboot and reprepare behavior of the driver:

- a node reboot must make the driver rebuild the CDI spec on the prepare path and hand the
  container the same CPUs, without recording prepare or unprepare errors;
- a kubelet-only restart must *not* trigger a second prepare, because the kubelet keeps the
  claim's devices and CDI IDs in its DRA manager state;
- a driver-only restart must leave the containers untouched and rebuild the driver state from
  the running containers, without a second prepare;
- deleting and recreating a pod bound to the same claim must release and re-acquire the CPUs;
  when the idle CPU pool is unchanged, the same CPUs are normally selected again, while
  grouped mode does not generally guarantee exact CPU reuse.

The kubelet API does not expose these faults, so the specs inject them by acting on the
container which backs the node, using the container runtime CLI configured through
`DRACPU_E2E_NODE_RUNTIME`. This works on kind-like clusters, where every node is a container
running on the same host as the test binary and named after the node. The specs skip
themselves when the runtime CLI is missing or when no container is named after the node.

Because a node reboot restarts the driver process, its counters start from zero again, so the
specs reason about the counters relatively rather than in absolute terms.

The driver behavior these specs pin down is described for users in
[how-it-works](../../docs/user/how-it-works.md#surviving-node-reboots-restarts-and-pod-recreation).

The suite waits on real cluster operations, so `make test-e2e` passes an explicit
`-timeout` (`DRACPU_E2E_TIMEOUT`, default `15m`). Running the suite with a direct `go test`
call needs the same, otherwise a stuck wait ends in the 10m default timeout panic instead of
a readable failure.

## how to run

The simplest way is from your terminal:

```bash
make test-e2e-kind
```

This creates a kind cluster from scratch, deploys the driver, and runs the full suite.
To run only specs labeled `metrics`, use:

```bash
DRACPU_E2E_LABEL_FILTER=metrics make test-e2e-kind
```

To run all specs except those labeled `negative` against an existing configured cluster:

```bash
DRACPU_E2E_LABEL_FILTER='!negative' make test-e2e
```

To run against an existing cluster with the driver already deployed:

```bash
make test-e2e
```

Note: `make test-e2e` does not build or load the test image; it only runs the Go e2e suite.
The `make test-e2e-kind` target builds and loads the image
automatically into a kind cluster, but for an arbitrary cluster the default `IMAGE_TEST` value
points at the local CI image name and will not be pullable. Either make sure the
test image is available to the cluster, or override the image explicitly:

```bash
DRACPU_E2E_TEST_IMAGE=<image> make test-e2e
```

### troubleshooting

- **Kubelet fails with `unknown service runtime.v1.RuntimeService`**

  The real error is in the containerd logs: `failed to create fsnotify watcher: too many open files`.
  The host's `fs.inotify.max_user_instances` (default 128) is exhausted.

  Fix: `sudo sysctl fs.inotify.max_user_instances=1024`, then recreate the cluster.

- **`kind load docker-image` fails with `no unpack platforms defined`**

  Happens with fuse-overlayfs storage driver and kind < v0.32.0
  ([kind#3945](https://github.com/kubernetes-sigs/kind/issues/3945).

  Fix: upgrade kind and rebuild the node image:

  ```bash
  go install sigs.k8s.io/kind@v0.32.0
  docker rmi kindest/node:<version>
  ```

- **Pods fail with `openat2 .../cpuset.cpus: no such file or directory`**

  When running kind with rootless Docker, the `cpuset` cgroup controller is not
  delegated to user sessions by default. The NRI hook sets `cpuset.cpus` on every
  container, so all pods (including CoreDNS) crash on startup.

  Verify by checking inside a kind node:

  ```bash
  docker exec <kind-node> cat /sys/fs/cgroup/cgroup.controllers
  # If "cpuset" is missing from the output, this is the issue.
  ```

  Fix:

  ```bash
  sudo mkdir -p /etc/systemd/system/user@.service.d
  sudo tee /etc/systemd/system/user@.service.d/delegate-cpuset.conf <<EOF
  [Service]
  Delegate=cpuset cpu io memory pids
  EOF
  sudo systemctl daemon-reload
  ```

  Then recreate the kind cluster.
