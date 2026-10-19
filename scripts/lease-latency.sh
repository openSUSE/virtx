#!/bin/bash
#
# lease-latency.sh -- READ-ONLY: how long does a lease-path read take right now?
# It only READS the lockspace file (sanlock direct read_leader); it changes nothing.
#
set -u
LS=__VIRTX__DISKS__
LF=/vms/lock/$LS
REG=/vms/reg
N=${1:-20}

# find our host uuid by looking for the reg dir whose lockid matches our own host_id
OUR_ID=$(sanlock client gets 2>/dev/null | awk '$1 == "s" { split($2, a, ":"); print a[2]; exit }')
if [ -z "${OUR_ID:-}" ]; then
	# no lockspace joined: fall back to the first host dir that has a lockid
	OUR_ID=$(tr -d '[:space:]' < "$(ls -d $REG/*/ | head -1)lockid" 2>/dev/null)
fi
HOST_DIR=$(grep -rl "^$OUR_ID\$" $REG/*/lockid 2>/dev/null | head -1)
HOST_UUID=$(basename "$(dirname "$HOST_DIR")")

echo "== lease-path read latency on $(hostname) =="
echo "lockspace file: $LF   host_id: ${OUR_ID:-?}   host_uuid: ${HOST_UUID:-?}"
i=0; min=999999999; max=0; sum=0; bad=0
while [ $i -lt "$N" ]; do
	t0=$(date +%s%N)
	if ! sanlock direct read_leader -s "$LS:$OUR_ID:$LF:0" > /dev/null 2>&1; then
		bad=$(( bad + 1 ))
	fi
	t1=$(date +%s%N)
	d=$(( (t1 - t0) / 1000000 ))
	printf 'sample %2d: %6d ms\n' "$i" "$d"
	[ "$d" -lt "$min" ] && min=$d
	[ "$d" -gt "$max" ] && max=$d
	sum=$(( sum + d ))
	i=$(( i + 1 ))
done
echo "min=${min}ms  max=${max}ms  avg=$(( sum / N ))ms  failed_reads=${bad}"
