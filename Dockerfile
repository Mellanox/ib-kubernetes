FROM golang:alpine as builder

ADD . /usr/src/ib-kubernetes

ENV HTTP_PROXY $http_proxy
ENV HTTPS_PROXY $https_proxy

RUN apk add --update --virtual build-dependencies build-base linux-headers && \
    cd /usr/src/ib-kubernetes && \
    make clean && \
    make build

FROM alpine
COPY --from=builder /usr/src/ib-kubernetes/build/ib-kubernetes /usr/bin/
WORKDIR /

LABEL io.k8s.display-name="InfiniBand Kubernetes"

CMD ["/usr/bin/ib-kubernetes"]
