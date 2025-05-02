FROM golang:1.24 AS builder

WORKDIR /workspace
ADD ./go.mod ./
ADD ./go.sum ./
RUN go mod download

ADD ./ ./
RUN make all

FROM nvcr.io/nvidia/distroless/go:v3.1.6
LABEL org.opencontainers.image.source=https://nvcr.io/nvidia/cloud-native/multus-cni
WORKDIR /
# Add everything
ADD . /workspace

LABEL io.k8s.display-name="InfiniBand Kubernetes"

CMD ["/ib-kubernetes"]
