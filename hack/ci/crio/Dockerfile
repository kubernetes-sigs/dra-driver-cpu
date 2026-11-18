# check=skip=InvalidDefaultArgInFrom
# Based on CRI-O's tutorials/crio-in-kind.md. Versions are set in the Makefile
# (CRIO_BASE_NODE_IMAGE, CRIO_MINOR, CRIO_PACKAGE_VERSION) via build-node.sh.
ARG NODE_IMAGE
FROM ${NODE_IMAGE}
ARG CRIO_MINOR
ARG CRIO_PACKAGE_VERSION
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl gnupg \
    && curl -fsSL "https://download.opensuse.org/repositories/isv:/cri-o:/stable:/${CRIO_MINOR}/deb/Release.key" \
       | gpg --dearmor -o /etc/apt/keyrings/crio.gpg \
    && echo "deb [signed-by=/etc/apt/keyrings/crio.gpg] https://download.opensuse.org/repositories/isv:/cri-o:/stable:/${CRIO_MINOR}/deb/ /" > /etc/apt/sources.list.d/crio.list \
    && apt-get update && apt-get -o Dpkg::Options::="--force-confold" install -y --no-install-recommends "cri-o=${CRIO_PACKAGE_VERSION}" podman \
    && rm -rf /var/lib/apt/lists/* \
    && systemctl disable containerd && systemctl enable crio
COPY crio.conf /etc/crio/crio.conf.d/99-dra-ci.conf
RUN printf 'runtime-endpoint: unix:///var/run/crio/crio.sock\nimage-endpoint: unix:///var/run/crio/crio.sock\n' > /etc/crictl.yaml
