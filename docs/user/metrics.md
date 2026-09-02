# Metrics

The driver exposes Prometheus metrics on the existing HTTP `/metrics` endpoint served by `--bind-address` (default `:8080`).

> [!NOTE]
> Driver custom metrics are ALPHA. They are useful for early observability, but metric names, labels, buckets, and semantics may change in future releases.

Custom driver metrics can also be listed programmatically without starting the driver:

```bash
dracpu introspect metrics
```

The command prints JSON metadata for custom `dra_cpu_*` metrics only. It does not include default Go runtime, process, or Prometheus client metrics.

| Metric                                     | Type      | Labels   | Description                                                                                |
| ------------------------------------------ | --------- | -------- | ------------------------------------------------------------------------------------------ |
| `dra_cpu_allocated_cpus`                   | Gauge     | none     | CPUs currently allocated to prepared resource claims.                                      |
| `dra_cpu_available_cpus`                   | Gauge     | none     | CPUs still available for allocation after reserved and active claim CPUs are excluded.     |
| `dra_cpu_reserved_cpus`                    | Gauge     | none     | CPUs excluded from DRA management by driver configuration.                                 |
| `dra_cpu_resource_claims_active`           | Gauge     | none     | Resource claims currently recorded as active by the allocation store.                      |
| `dra_cpu_prepare_claims_total`             | Counter   | `result` | Per-claim `PrepareResourceClaims` results. `result` is `success`, `error`, or `unknown`.   |
| `dra_cpu_unprepare_claims_total`           | Counter   | `result` | Per-claim `UnprepareResourceClaims` results. `result` is `success`, `error`, or `unknown`. |
| `dra_cpu_prepare_claim_duration_seconds`   | Histogram | none     | Per-claim prepare latency in seconds.                                                      |
| `dra_cpu_unprepare_claim_duration_seconds` | Histogram | none     | Per-claim unprepare latency in seconds.                                                    |
| `dra_cpu_claim_allocated_cpus`             | Histogram | none     | CPUs allocated for each newly successful claim allocation.                                 |

The custom metrics intentionally avoid labels for namespace, pod, claim, device, node, socket, NUMA node, group mode, and error reason. Those labels would either be high-cardinality or need more API design before becoming part of the driver's metric surface. Node identity should come from scrape target labels.
