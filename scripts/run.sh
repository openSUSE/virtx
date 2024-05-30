#!/bin/bash

set -euxo pipefail

NAME=${NAME:-"$(hostname -s)"}
IMAGE=${IMAGE:-"inventory-service:devel"}
SERF_AGENT_ARGS=${SERF_AGENT_ARGS:=""}

docker run --rm -ti --name ${NAME} \
    -v /var/run/libvirt:/var/run/libvirt:ro \
    -p 8080:8080/tcp \
    -p 7946:7946/tcp \
    -p 7946:7946/udp \
    -e SERF_AGENT_ARGS="${SERF_AGENT_ARGS}" \
    ${IMAGE}
