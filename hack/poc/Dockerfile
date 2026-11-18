# Copyright 2026 The Kubernetes Authors.
# SPDX-License-Identifier: Apache-2.0
# Binaries are built against the pinned Kubernetes staging workspace, not in Docker.
FROM gcr.io/distroless/base-debian12
COPY bin/dracpu /dracpu
COPY bin/dracputester /dracputester
CMD ["/dracpu"]
