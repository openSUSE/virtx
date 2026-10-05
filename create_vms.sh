#! /bin/bash
#
# create_vms.sh COUNT SOURCE [PREFIX]
#
# Create COUNT VMs on each host of the cluster (they are not started),
# VMs are named PREFIX-NNN (default PREFIX: scale) and tagged with the custom
# field SCALE=PREFIX.
#
# Environment overrides:
#   SOURCE  gold image to provision the OS disk from
#   HOSTS   host UUIDs to use (default: all hosts in "virtx list host")
#   VCPUS   vcpus per VM (default: 2)
#   MEMORY  MiB of memory per VM (default: 2048)
#   DISK    MiB of OS disk per VM (default: 20480)
#   DS      directory of the OS disks (default: /vms/ds)
#
# Requires VIRTX_API_SERVER to be set, and ./virtx

set -u

if [ $# -lt 1 ]; then
    echo "usage: $0 COUNT [PREFIX]" >&2
    exit 1
fi
COUNT=$1
PREFIX=${2:-scale}
SOURCE=${SOURCE:-}
VCPUS=${VCPUS:-2}
MEMORY=${MEMORY:-2048}
DISK=${DISK:-20480}
DS=${DS:-/vms/ds}
HOSTS=${HOSTS:-$(./virtx list host | awk 'NR > 1 { print $1 }')}

# create_host HOST FIRST: create COUNT VMs on HOST, numbered from FIRST
create_host() {
    local host=$1 n=$2 i name failed=0
    for i in $(seq 1 "$COUNT"); do
        name=$(printf "%s-%03d" "$PREFIX" "$n")
        n=$((n + 1))
        if ! ./virtx create vm /dev/stdin -h "$host" > /dev/null <<EOF
{
    "name": "$name",
    "cpudef": { "arch": "x86_64", "model": "host-passthrough", "sockets": 1, "cores": $VCPUS, "threads": 1 },
    "memory": { "total": $MEMORY, "hp": false },
    "osdisk": {
        "path": "$DS/$name.qcow2",
        "device": 0, "bus": 0, "man": 1, "prov": 1,
        "size": $DISK,
        "source": "$SOURCE"
    },
    "nets": [ { "name": "default", "nettype": 0, "model": 0, "mac": "" } ],
    "firmware": 1,
    "custom": [ { "name": "SCALE", "value": "$PREFIX" } ]
}
EOF
        then
            echo "$name FAILED on $host" >&2
            failed=$((failed + 1))
        fi
    done
    echo "$host: $((COUNT - failed)) VMs created, $failed failed"
}

first=1
for host in $HOSTS; do
    create_host "$host" "$first" &
    first=$((first + COUNT))
done
wait
