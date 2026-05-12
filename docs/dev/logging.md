# logging guidelines

## picking a verbosity level

V(0): high-signal operational logs and errors always visible at default verbosity
V(2): normal operation, important decisions, summary
V(4): allocation internals, fine details about decisions
V(6): debug messages, very fine details, mostly for developers

V(7) or more is reserved for future usage.

## the opID tag

`opID` is a cheap trace identifier similar in spirit to OTEL's
(OTEL=OpenTelemetry) `trace_id`. Same concept, applied to logs:
a unique identifier constantly
logged in all the entries pertaining a flow, which trivially
enable to extract all the logs with `grep`.

Other examples sit in `test/e2e/contextual_logging_test.go`
and in `test/pkg/logcheck/`.

We reimplement because the concept is super cheap and super useful
for logs, even without telemetry implementation.

If in the future we integrate with OTEL, we will add OTEL's
identifier in addition to the current opID for a transitional
period, then we will remove our homegrown solution.

### sizing opID

Since we use it everywhere, we need to minimize the log noise,
so we set the value of `opIDLen` to "only" 8 hex char.
We evaluate this value to still provide plenty of
randomness for a *local only* (not distributed) identifier.

Here's the back-of-envelope math:

- 12 hex chars = 48 bits → ~281 trillion values
- 8 hex chars = 32 bits → ~4.3 billion values

The probability of at least one collision among n operations is roughly n\*\*2/(2N).
With 8 chars:

- 1000 operations: ~0.01% collision probability.
- 10000 operations: ~1.2% collision probability.
