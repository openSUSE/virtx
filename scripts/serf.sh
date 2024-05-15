#!/bin/bash

set -euxo pipefail

IMAGE=test-serf:devel
MAX_NODE=31
NAME_FMT=serf%02u

function start() {
    _name=$(printf ${NAME_FMT} 0)
    docker run --rm --detach \
        --name ${_name} \
        --hostname ${_name} \
        ${IMAGE} agent
    _addr=$(docker inspect --format '{{ .NetworkSettings.IPAddress }}' ${_name})

    for i in $(seq ${MAX_NODE}); do
        _name=$(printf $NAME_FMT ${i})
        docker run --rm --detach \
            --name ${_name} \
            --hostname ${_name} \
            ${IMAGE} agent -join=${_addr}
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
