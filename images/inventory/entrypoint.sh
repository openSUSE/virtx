#!/bin/bash

set -euxo pipefail

SERF_AGENT_ARGS=${SERF_AGENT_ARGS:-}

serf agent ${SERF_AGENT_ARGS} &> /tmp/serf.log &

# Give serf some time to start and join the cluster
sleep 5

inventory-service
