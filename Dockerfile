FROM golang:1.24 AS builder

WORKDIR /workspace
ADD ./go.mod ./
ADD ./go.sum ./
RUN go mod download

ADD ./ ./
RUN make all

FROM gcr.io/distroless/base
WORKDIR /
COPY --from=builder /workspace/build/ib-kubernetes /
COPY --from=builder /workspace/build/plugins /plugins

LABEL io.k8s.display-name="InfiniBand Kubernetes"

CMD ["/ib-kubernetes"]

LABEL org.opencontainers.image.source=https://github.com/Mellanox/ib-kubernetes
