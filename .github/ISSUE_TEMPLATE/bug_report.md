---
name: Bug Report
about: Report a bug encountered while using DRA Driver CPU
labels: kind/bug
---

<!--
Please use this template while reporting a bug and provide as much info as possible.
Not doing so may result in your bug not being addressed in a timely manner. Thanks!

If the matter is security related, please disclose it privately via:
https://kubernetes.io/security/
-->

**What happened**:

**What you expected to happen**:

**How to reproduce it (as minimally and precisely as possible)**:

**Context / setup details**:

<!--
Describe the workload shape and any config that may affect driver behavior.
For example: ResourceClaim type, pod/container resources, CDI/NRI settings,
cpuManagerPolicy, cgroup version, etc.
-->

**Relevant logs or outputs**:

<!--
Please include relevant snippets from:
- DRA driver logs
- kubelet logs (if relevant)
- container runtime logs (if relevant)
- `kubectl describe pod`, `kubectl get resourceclaims`, etc.
-->

```text
# paste logs/output here
```

**Question / what you want to verify**:

<!--
If you are unsure this is a bug, describe what behavior or configuration you
want maintainers to validate.
-->

**Diagnostics (`dracpu-gatherinfo`)**:

<!--
Please run `dracpu-gatherinfo` from the affected driver pod and attach the output.
Single node example:

kubectl exec -n dra-driver-cpu <dracpu-pod> -- /dracpu-gatherinfo --stdout

For multi-node issues, collect one report per affected node.
For more details, see:
https://github.com/kubernetes-sigs/dra-driver-cpu/blob/main/docs/gatherinfo.md
-->

```text
# paste dracpu-gatherinfo output here
```

**Environment**:
- DRA driver version/tag (image tag or commit SHA):
- DRA driver mode (`grouped` or `individual`):
- DRA grouped mode setting (`numanode` or `socket`, if applicable):
- Container runtime and version (optional):
- Kubernetes version (use `kubectl version`):
- Cloud provider or hardware configuration:
- OS (e.g. `cat /etc/os-release`):
- Kernel (e.g. `uname -a`):
- Network plugin and version (if this is a network-related bug):
- Others:
