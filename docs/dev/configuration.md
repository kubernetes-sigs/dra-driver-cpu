# Adding configuration tunables

The configurable behavior of the DRA CPU driver can be changed using two approaches:

- command line flags
- configuration file

The preferred way to add or extend the configurable behavior is through the
configuration file. The reasons why we prefer this approach are:

- user friendliness: adding options quickly makes the command line long and awkward
- auditable, reviewable artifact: the configuration file can be versioned, compared
  and verified more easily
- extensibility: it's usually easier to process configuration files with programs
  than command line options
- configuration files are easier to version and evolve compatibly than command
  line flags.

Configuration values are applied incrementally:

- the driver has compiled in defaults
- the defaults are overridden by the configuration file, resulting in a merged configuration
- the explicitly set command line flags override the merged configuration, resulting in the
  final configuration which will be used.

**NOTE**: only flags that are actually passed on the command line take precedence; the unset
flags inherit the prior value from the previous incremental layer (or the default).

The current configuration-file first approach has a pretty large consensus, and there is no
obvious reason to change again in the foreseeable future, but is not written in stone;
we may revisit this stance in the future should the circumstances change.

Adding command line options is however **NOT** prohibited, but should be approached
with a healthy dose of skepticism.

If you are trying to add configurable behavior to the driver, a few questions can
help your decision making process:

- does the behavior need to be configured at all? Can we just change the defaults?
- is the option more like a feature flag or feature gate or something users may genuinely
  decide to flip depending on their setup?
  should you need a feature flag/gate, please get in touch to discuss the best approach.
- some tunables are needed because of the nature of the driver, which wants to run
  as containerized process as main delivery vehicle. If your option fits in this category,
  you may need no driver option at all, but a new helm chart value or a manifest change.
- is your option a tunable knob of the current operational mode? if so it belongs
  in the configuration file.
- is your option an operational-mode switch - a flag that changes what the driver does
  rather than tune the running driver? if so, a command line flag is likely warranted.

If we need to add operational modes, we express them as subcommands (e.g.
`dracpu introspect metrics` instead of `dracpu --show-metrics`), using a lightweight
stdlib-based dispatch internal package rather than a full CLI framework
(see [#234](https://github.com/kubernetes-sigs/dra-driver-cpu/issues/234),
[#131](https://github.com/kubernetes-sigs/dra-driver-cpu/pull/131)).
An internal lightweight package serves our current and foreseeable future needs well.
We don't have yet the need for a much heavier package like `cobra`.

As a concrete example: we briefly introduced the `--show-metrics` command line flag.
But this was quickly determined as operational-mode switch - it prints
metadata and exits instead of running the driver. Exposing it in the configuration file
led to a silent failure: the value was loaded and logged, but the print-and-exit handler
ran before the configuration file was read, so the setting was silently inert on the Helm path
(see [#234](https://github.com/kubernetes-sigs/dra-driver-cpu/issues/234)).

Please make sure to document your rationale (no need to write an essay: a few clear sentences
are perfectly fine) in your issue/PR so we can evaluate and converge towards the best shape.

## Handling multiple configuration version

See the [backward compatibility policy](../user/upgrade.md#backward-compatibility-policy)
for the project-wide promise.

### Concepts

The main versioned configuration source are the configuration files.
The command line flags remain **unversioned**, and their backward compatibility
is best effort. We are (0.3.0 at time of writing) aggressively phasing out all the
command line tunables, which mitigates the compatibility concerns.

We adopt the same approach [scheduler-plugins](https://github.com/kubernetes-sigs/scheduler-plugins/tree/master/apis/config)
follows: an internal runtime configuration and one or more public versioned
configuration schemas. While we reuse the same conceptual model, the implementation
will however be the simplest possible which serves our needs.

Each supported version will be converted to the internal configuration, following
a "hub and spoke"-like mode, without converting between public versions.
For example, if at a given time we support `v1alpha3` and `v1beta1` configuration
APIs we will have:

- `v1alpha3` -> internal
- `v1beta1` -> internal

and **not**: `v1alpha3` -> `v1beta1`-> internal

The runtime code will only consume only the internal configuration and does not
branch on the input versions.
The internal configuration should never be exposed directly to external consumers,
because there is not any stability promise, not even across Z version.
The tooling needed to extract the configuration will always and only produce
the latest supported version; in the example above, it will be `v1beta1`.

### Loading configuration

We keep the model on which each configuration source is an overlay over the
previous settings.
This will make configlet support very easy to add later on and costs little
extra complexity.

The basic load flow for _configuration_ files will look like this:

1. Identify the version and strictly decode its public schema. IOW, we will not try
   each decoder till one succeeds (compare to how kubelet state files are managed).
1. Apply that version's defaults and convert to the internal configuration.
1. Only the final effective configuration will be validated according to the
   *internal representation constraints* (regardless of the versioned source)

Defaults for public fields belong to their schema version, not to core runtime
code. The backward compatibility is therefore delegated to the versioned schemas.
The backward compatibility promises extends also to the _meaning_ of the
settings and to supplied and omitted fields; accepting an old file is insufficient
if its behavior changes silently.

When no configuration file is specified, we will use the most recent schema to supply
the current defaults.

Decoding must reject unknown fields, duplicate keys, and incorrect key casing.
Define omission, explicit zero or empty values, and `null` deliberately; use pointer
fields where presence affects semantics. Versionless files always mean legacy
`v1alpha1`, never the latest schema. New formats require an explicit `apiVersion`.
Note the assumption about missing schemas is only limited to `v1alpha1` and will
be dropped once `v1alpha1` support is phased out.

### Version skew

We support the current schema and **the preceding schema**, ordered by
introduction, independently of driver releases. For example, adding `v1alpha2`
retains `v1alpha1`; adding `v1alpha3` will cause `v1alpha1` to be retired.

Schemas are added on their own lifecycle on a per-need basis and these are *not*
tied to the release process: a new version may or may not introduce new schemas,
and in the long run we expect the schemas to stabilize and change less often,
if at all.

Unsupported versions fail startup with the supported versions and migration
guidance. The driver will **not** automatically convert, edit or mutate
configuration files;
we reserve the options to add conversion helpers in separate binaries.

While a schema is supported, the runtime must retain the behavior
necessary to honor it.

Helm validation must derive from the public schemas and accept every supported
version. Rendering and deprecated argument folding must respect the selected
schema, without silently changing its version. New examples and generated files
must carry an explicit version. Document migration steps and chart/image
compatibility, including rollback: an older binary generally cannot read a newer
schema, so retain a mutually supported format until deliberately migrating.
