---
title: User Documentation
linkTitle: User
weight: 10
description: Install, configure and operate the DRA Driver for CPU.
type: docs
github_subdir: "site"
cascade:
  type: docs
  github_subdir: ""
  path_base_for_github_subdir:
    from: '^content/docs/(user|dev)/([^_].*)$'
    to: 'docs/$1/$2'
---

Start with the [Quickstart](quickstart.md) to install the driver and run a pod on
exclusive CPUs, then continue with [Installation](installation.md) and
[Configuration](configuration.md) for the environment-specific setup.

See [Feature Support](feature-support.md) for how the driver maps to the kubelet
CPU Manager options, and [Troubleshooting & Diagnostics](troubleshooting.md) when
something does not behave as expected.
