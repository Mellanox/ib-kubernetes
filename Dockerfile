FROM golang:alpine as builder

WORKDIR /workspace
ADD ./ ./

ENV HTTP_PROXY $http_proxy
ENV HTTPS_PROXY $https_proxy

RUN apk add --update --virtual build-dependencies build-base binutils linux-headers git
RUN make

FROM alpine
WORKDIR /
COPY --from=builder /workspace/build/ib-kubernetes /
COPY --from=builder /workspace/build/plugins /plugins

LABEL io.k8s.display-name="InfiniBand Kubernetes"

CMD ["/ib-kubernetes"]

LABEL org.opencontainers.image.source=https://github.com/Mellanox/ib-kubernetes
