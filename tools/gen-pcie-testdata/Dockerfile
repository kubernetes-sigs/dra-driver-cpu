# use this Dockerfile if you want an image to run
# in your cluster to extract test data for this driver

# setup cross-compile env
FROM --platform=$BUILDPLATFORM golang:1.26 AS builder
ARG TARGETARCH
ARG GOARCH=${TARGETARCH} CGO_ENABLED=0
ARG LDFLAGS=-s -w

# cache go modules
WORKDIR /go/src/app
COPY go.mod go.sum .
RUN go mod download

# build
COPY . .
RUN go build -ldflags "${LDFLAGS}" -o /go/bin/gen ./tools/gen-pcie-testdata/

# copy binary onto base image
FROM gcr.io/distroless/base-debian12
COPY --from=builder --chown=root:root /go/bin/gen /gen
CMD ["/gen"]
