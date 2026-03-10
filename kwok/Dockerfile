FROM registry.k8s.io/kwok/cluster:v0.5.1-k8s.v1.27.3

ENV KWOK_CLUSTER=kwok

COPY --chmod=644 inittab /etc/inittab

RUN KWOK_KUBE_APISERVER_PORT=0 kwokctl create cluster --name "$KWOK_CLUSTER" || exit 1 && \
    kwokctl stop cluster --name="$KWOK_CLUSTER"

ENTRYPOINT ["/sbin/init"]
