#! /bin/bash
#
# delete_vms.sh [PREFIX]
#
# Delete the VMs created by create_vms.sh (custom field SCALE), including
# their managed storage. With PREFIX, only the VMs created with that PREFIX.
# VMs not created by create_vms.sh are never touched.
# VMs are listed and deleted host by host, one parallel stream per host.
#
# Requires VIRTX_API_SERVER to be set, and ./virtx

set -u

FILTER=( -N SCALE )
if [ $# -ge 1 ]; then
    FILTER+=( -V "$1" )
fi

delete_host() {
    local host=$1 uuid n=0 failed=0
    for uuid in $(./virtx list vm -h "$host" "${FILTER[@]}" | awk 'NR > 1 { print $1 }'); do
        n=$((n + 1))
        if ! ./virtx delete vm "$uuid" -s > /dev/null; then
            echo "$uuid FAILED on $host" >&2
            failed=$((failed + 1))
        fi
    done
    echo "$host: $((n - failed)) VMs deleted, $failed failed"
}

for host in $(./virtx list host | awk 'NR > 1 { print $1 }'); do
    delete_host "$host" &
done
wait
