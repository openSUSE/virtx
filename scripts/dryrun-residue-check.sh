#!/bin/sh
# VirtX HA Slice B: the "a drill leaves no trace" check (acceptance item 7).
#
# A drill moves one VM's registration directory to the landing host and moves it
# straight back. This script snapshots the whole registration tree so the two
# states can be compared byte for byte.
#
# usage: dryrun-residue-check.sh <tag> before|after
#   <tag> is any label, e.g. the VM uuid or "drill1".
#
# It writes /root/dryrun-residue-<tag>-<phase>.txt containing, sorted:
#   <path relative to /vms/reg> <size> <sha256 of the file>
# and prints the sha256 of that listing, which is what you compare.
set -e

tag="$1"
phase="$2"
if [ -z "$tag" ] || [ -z "$phase" ]; then
	echo "usage: $0 <tag> before|after" >&2
	exit 2
fi

out="/root/dryrun-residue-$tag-$phase.txt"
tmp="$out.tmp"

# %P is the path relative to /vms/reg; hash every file so content changes count.
find /vms/reg -type f -printf '%P\0' | sort -z | while IFS= read -r -d '' rel; do
	sum=$(sha256sum "/vms/reg/$rel" | cut -d' ' -f1)
	size=$(stat -c %s "/vms/reg/$rel")
	printf '%s %s %s\n' "$rel" "$size" "$sum"
done > "$tmp"
mv "$tmp" "$out"

echo "wrote $out ($(wc -l < "$out") files)"
sha256sum "$out"
