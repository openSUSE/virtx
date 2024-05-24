#!/bin/bash

set -euxo pipefail

IMAGE=inventory-service:devel
MAX_NODE=4
NAME_FMT=node%02u

function start() {
    _name=$(printf ${NAME_FMT} 0)
    docker run --rm --detach \
        --name ${_name} \
        --hostname ${_name} \
        --volume /var/run/libvirt:/var/run/libvirt:ro \
        --publish 8080:8080/tcp \
        ${IMAGE}
    _addr=$(docker inspect --format '{{ .NetworkSettings.IPAddress }}' ${_name})

    for i in $(seq 1 ${MAX_NODE}); do
        _name=$(printf $NAME_FMT ${i})
        docker run --rm --detach \
            --name ${_name} \
            --hostname ${_name} \
            --volume /var/run/libvirt:/var/run/libvirt:ro \
            --env SERF_AGENT_ARGS="-join=${_addr}" \
            ${IMAGE}
    done
}

function stop() {
    set +e
    for i in $(seq 0 ${MAX_NODE}); do
        _name=$(printf $NAME_FMT ${i})
        docker kill ${_name}
    done
    set -e
}

case ${1} in
    start)
        start
        ;;
    stop)
        stop
        ;;
esac
