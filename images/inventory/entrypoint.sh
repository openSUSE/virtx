#!/bin/bash

set -euxo pipefail

SERF_AGENT_ARGS=${SERF_AGENT_ARGS:-}

serf agent ${SERF_AGENT_ARGS} &> /tmp/serf.log &

inventory-service
