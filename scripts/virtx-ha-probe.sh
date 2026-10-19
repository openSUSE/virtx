#!/bin/bash
#
# virtx-ha-probe.sh -- READ-ONLY probe: sanlock leases + host view, for VirtX HA.
#
# It answers three questions:
#   disk      : who holds which lease, on shared storage (readable from ANY host)
#   local     : what this host's sanlock daemon actually thinks (config + host states)
#   can-boot  : can this VM start somewhere else? and if not, what is missing?
#
# It NEVER acquires or releases a lease, NEVER writes to storage, NEVER changes config.
# The only sanlock subcommands used are: direct read_leader, client gets, client status.
#
# Judgement table (see "can-boot"):
#   resource lease timestamp == 0            -> free      -> may start
#   timestamp != 0 but resource owner_generation < that host_id's current generation
#                                            -> STALE     -> the record predates the
#                                               holder's current host_id; may start
#   held (generation matches), holder LIVE   -> no        -> holder is writing
#   held, holder's host_id state UNKNOWN/FAIL-> not yet   -> wait
#   held, holder's host_id state DEAD        -> may start (sanlock's own acquire decides)
#   resource file missing                    -> no        -> create it first
# NOT done in this version: liveness from the on-disk records alone. The host_id
# timestamp is a *monotime* of the writer's boot, so it must not be compared with
# this host's clock. Liveness here comes from a joined host's own view (gets -h 1).
#
set -u

LOCK_DIR=/vms/lock
REG_DIR=/vms/reg
DS_DIR=/vms/ds/
LS=__VIRTX__DISKS__
# The two below can be pointed at STAND-IN files, purely for testing the judgement
# branches without touching a real host:
#   WDEV_DIR    = directory containing "state" and "timeout" of the watchdog device
#   HOSTIO_FILE = stand-in for the per-host table, lines "<host_uuid> <host_id> <io_timeout>"
WDEV_DIR=${WDEV_DIR:-/sys/class/watchdog/watchdog0}
HOSTIO_FILE=${HOSTIO_FILE:-}
SANLOCK_CONF=/etc/sanlock/sanlock.conf

die() { printf 'ERROR: %s\n' "$*" >&2; exit 2; }

raw_leader() {  # $1 = -s|-r, $2 = spec
	sanlock direct read_leader "$1" "$2" 2>&1
}

field() {  # $1 = raw text, $2 = key
	printf '%s\n' "$1" | awk -v k="$2" '$1 == k { print $2; exit }'
}

host_uuids() {
	local d b
	for d in "$REG_DIR"/*/; do
		b=$(basename "$d")
		[ -f "$REG_DIR/$b/lockid" ] || continue
		printf '%s\n' "$b"
	done
}

host_id_of() {  # $1 = host uuid
	tr -d '[:space:]' < "$REG_DIR/$1/lockid" 2>/dev/null
}

res_names() {
	local d b
	for d in "$LOCK_DIR"/*/; do
		b=$(basename "$d")
		[ -f "$LOCK_DIR/$b/$b" ] || continue
		printf '%s\n' "$b"
	done
}

live_states() {  # "<host_id> <STATE>" from THIS host's own view
	sanlock client gets -h 1 2>/dev/null | awk '$1 == "h" { print $2, $NF }'
}

state_of() {  # $1 = host_id -> STATE (empty if this host does not know it)
	live_states | awk -v id="$1" '$1 == id { print $2; exit }'
}

daemon_cfg() {  # $1 = key
	sanlock client status -D 2>/dev/null | awk -F= -v k="$1" '$1 ~ ("^[[:space:]]*" k "$") { gsub(/[[:space:]]/, "", $2); print $2; exit }'
}

host_gen() {  # $1 = host_id -> that host's current host_id generation
	raw_leader -s "$LS:$1:$LOCK_DIR/$LS:0" | awk '$1 == "owner_generation" { print $2; exit }'
}

host_io_table() {  # "<host_uuid> <host_id> <io_timeout>" for every host
	local h id raw
	if [ -n "$HOSTIO_FILE" ]; then
		cat "$HOSTIO_FILE"
		return
	fi
	for h in $(host_uuids); do
		id=$(host_id_of "$h")
		[ -n "$id" ] || continue
		raw=$(raw_leader -s "$LS:$id:$LOCK_DIR/$LS:0")
		printf '%s %s %s\n' "$h" "$id" "$(field "$raw" io_timeout)"
	done
}

check_tiers() {  # print each host's io_timeout; return 1 if they are not all equal
	local tbl first v bad=0
	tbl=$(host_io_table)
	printf '%s\n' "$tbl" | while read -r u i v; do
		[ -n "${v:-}" ] || continue
		printf '  %-34s host_id %-6s io_timeout %s\n' "$u" "$i" "$v"
	done
	first=$(printf '%s\n' "$tbl" | awk 'NF == 3 { print $3; exit }')
	while read -r u i v; do
		[ -n "${v:-}" ] || continue
		if [ "$v" != "$first" ]; then
			printf '  !! INCONSISTENT: %s has io_timeout %s, others have %s\n' "$u" "$v" "$first"
			bad=1
		fi
	done < <(printf '%s\n' "$tbl")
	return "$bad"
}

lease_state() {  # $1 = resource name -> FREE | STALE(gen a<b) | HELD <owner_id> | UNREADABLE
	local raw ts owner rgen hgen
	raw=$(raw_leader -r "$LS:$1:$LOCK_DIR/$1/$1:0")
	ts=$(field "$raw" timestamp)
	owner=$(field "$raw" owner_id)
	rgen=$(field "$raw" owner_generation)
	if [ -z "$ts" ]; then printf 'UNREADABLE'; return; fi
	if [ "$ts" = 0 ]; then printf 'FREE'; return; fi
	hgen=$(host_gen "$owner")
	# A resource record older than the holder's current host_id generation is a
	# leftover: the holder re-acquired its host_id after that record was written.
	if [ -n "$hgen" ] && [ -n "$rgen" ] && [ "$rgen" -lt "$hgen" ] 2>/dev/null; then
		printf 'STALE(gen %s<%s)' "$rgen" "$hgen"
		return
	fi
	printf 'HELD %s' "$owner"
}

# ---------------------------------------------------------------- disk
cmd_disk() {
	local h id raw n st ts owner
	printf '== disk layer: lockspace %s, read on %s ==\n' "$LS" "$(hostname)"
	printf '\n-- host_id leases (shared storage) --\n'
	printf '%-34s %7s %9s %5s %10s %6s\n' HOST_UUID HOST_ID OWNER GEN TIMESTAMP IO_T
	for h in $(host_uuids); do
		id=$(host_id_of "$h")
		[ -n "$id" ] || continue
		raw=$(raw_leader -s "$LS:$id:$LOCK_DIR/$LS:0")
		printf '%-34s %7s %9s %5s %10s %6s\n' "$h" "$id" \
			"$(field "$raw" owner_id)" "$(field "$raw" owner_generation)" \
			"$(field "$raw" timestamp)" "$(field "$raw" io_timeout)"
	done
	printf '\n-- resource (per-disk) leases --\n'
	printf '%-34s %8s %10s %8s %12s %s\n' RESOURCE OWNER TIMESTAMP WRITE_ID WRITE_TS STATE
	for n in $(res_names); do
		raw=$(raw_leader -r "$LS:$n:$LOCK_DIR/$n/$n:0")
		ts=$(field "$raw" timestamp)
		owner=$(field "$raw" owner_id)
		if [ -z "$ts" ]; then
			printf '%-34s %8s %10s %8s %12s %s\n' "$n" - - - - UNREADABLE
			continue
		fi
		st=$(lease_state "$n")
		printf '%-34s %8s %10s %8s %12s %s\n' "$n" "$owner" "$ts" \
			"$(field "$raw" write_id)" "$(field "$raw" write_timestamp)" "$st"
	done
	printf '\nNOTE: HELD here means "the on-disk record says held, and its generation\n'
	printf 'matches the holder\x27s current host_id". Whether that holder is alive is a\n'
	printf 'separate question -- ask a joined host (local mode, gets -h 1). The host_id\n'
	printf 'timestamp is the writer\x27s own monotime: never compare it with a local clock.\n'
}

# --------------------------------------------------------------- local
cmd_local() {
	local uuid id
	uuid=$(sanlock client status 2>/dev/null | awk '$1 == "daemon" { print $2; exit }')
	id=$(host_id_of "${uuid%%.*}")
	printf '== local view on %s ==\n' "$(hostname)"
	printf 'host uuid          : %s\n' "${uuid%%.*}"
	printf 'host_id            : %s\n' "${id:-unknown}"
	printf 'use_watchdog       : %s\n' "$(daemon_cfg use_watchdog)"
	printf 'io_timeout         : %s\n' "$(daemon_cfg io_timeout)"
	printf 'watchdog_fire_tmo  : %s\n' "$(daemon_cfg watchdog_fire_timeout)"
	printf 'kill_grace_seconds : %s\n' "$(daemon_cfg kill_grace_seconds)"
	printf 'sanlock version    : %s\n' "$(daemon_cfg version_str)"
	printf 'conf file          : %s\n' "$(grep -h '^ *use_watchdog' "$SANLOCK_CONF" 2>/dev/null | tr -s ' ')"
	printf 'watchdog dev state : %s\n' "$(cat "$WDEV_DIR/state" 2>/dev/null)"
	printf 'watchdog dev raw tmo: %s\n' "$(cat "$WDEV_DIR/timeout" 2>/dev/null)"
	printf '\n-- this host\x27s view of the cluster (gets -h 1) --\n'
	printf '%-8s %s\n' HOST_ID STATE
	live_states
}

# ------------------------------------------------------------ can-boot
cmd_can_boot() {
	local vm=$1 dir xml owner_host
	if [ -f "$vm" ]; then		# an XML file given directly (for testing)
		xml="$vm"; owner_host="(from the XML file given)"
	else
		for dir in "$REG_DIR"/*/"$vm"; do
			[ -f "$dir/$vm.xml" ] || continue
			xml="$dir/$vm.xml"
			owner_host=$(basename "$(dirname "$dir")")
		done
		[ -n "${xml:-}" ] || die "no registration XML for VM $vm (looked under $REG_DIR/*/$vm/)"
	fi
	printf '== can-boot: VM %s ==\n' "$vm"
	printf 'registered under host: %s\n' "$owner_host"
	local keys k st verdict=0
	keys=$(sed -n 's:.*<key>\([^<]*\)</key>.*:\1:p' "$xml" | sort -u)
	if [ -z "$keys" ]; then
		printf '\nVM has NO <lease> element: every read/write disk would be unprotected\n'
		printf 'VERDICT: NO (unprotected)\n'
		exit 1
	fi
	printf '\n-- per-disk leases --\n'
	for k in $keys; do
		if [ ! -f "$LOCK_DIR/$k/$k" ]; then
			printf '%-34s %s\n' "$k" 'MISSING lease file -> NO'
			verdict=1
			continue
		fi
		st=$(lease_state "$k")
		case "$st" in
		FREE)
			printf '%-34s %s\n' "$k" 'FREE -> ok'
			;;
		STALE*)
			printf '%-34s %s\n' "$k" "$st -> leftover record, not a live holder -> ok"
			;;
		HELD*)
			local hid s
			hid=${st#HELD }
			s=$(state_of "$hid")
			printf '%-34s held by host_id %s (state: %s)\n' "$k" "$hid" "${s:-not-visible-here}"
			case "$s" in
			LIVE)     printf '%-34s %s\n' '' '-> NO: the holder is alive and writing'; verdict=1 ;;
			DEAD)     printf '%-34s %s\n' '' '-> may start (holder DEAD); sanlock acquire decides' ;;
			FREE)     printf '%-34s %s\n' '' '-> NO: lease says held but host_id lease is free (inconsistent)' ; verdict=1 ;;
			*|UNKNOWN|FAIL) printf '%-34s %s\n' '' '-> NOT YET: not enough evidence that the holder stopped'; verdict=1 ;;
			esac
			;;
		*)
			printf '%-34s %s\n' "$k" 'UNREADABLE -> NO (fail closed)'
			verdict=1
			;;
		esac
	done
	printf '\n-- disk devices vs leases --\n'
	printf '(a lease is NOT attached to any disk: the XML lists leases beside the disks,\n'
	printf ' so "one lease exists" does not mean "every disk is covered")\n'
	local dev man src key covered
	while IFS='|' read -r dev man src; do
		[ -n "${src:-}" ] || continue
		if [ "$dev" = lun ]; then
			printf '%-6s Man=%s %s\n' "$dev" "$man" "$src"
			printf '%-6s %s\n' '' '-> LUN: key comes from the device name, not checked here'
			continue
		fi
		key=$(printf '%s' "${src#$DS_DIR}" | md5sum | awk '{print $1}')
		case " $keys " in *" $key "*) covered=yes ;; *) covered=no ;; esac
		if [ "$covered" = yes ]; then
			printf '%-6s Man=%s %s\n' "$dev" "$man" "$src"
			printf '%-6s %s\n' '' "-> lease $key"
		else
			printf '%-6s Man=%s %s\n' "$dev" "$man" "$src"
			if [ "$dev" = cdrom ]; then
				printf '%-6s %s\n' '' '-> NO LEASE, but it is a cdrom (not written): acceptable'
			elif [ "$man" = U ]; then
				printf '%-6s %s\n' '' '-> NO LEASE and Man=Unmanaged: intended, but this disk is NOT protected'
			else
				printf '%-6s %s\n' '' '-> NO LEASE and not unmanaged: UNPROTECTED'
				verdict=1
			fi
		fi
	done < <(awk '
		/<disk / {
			inuse = 1; dev = "?"; man = "?"; src = "";
			if ($0 ~ /device=.cdrom./) dev = "cdrom";
			else if ($0 ~ /device=.disk./) dev = "disk";
			else if ($0 ~ /device=.lun./) dev = "lun";
		}
		inuse && /alias name=/ {
			l = $0; sub(/.*alias name=./, "", l); sub(/.\/>.*/, "", l); sub(/^..-/, "", l);
			split(l, f, "_"); man = substr(f[1], 1, 1);
		}
		inuse && /source file=/ { l = $0; sub(/.*file=./, "", l); sub(/.\/>.*/, "", l); src = l }
		inuse && /<\/disk>/ { printf "%s|%s|%s\n", dev, man, src; inuse = 0 }
	' "$xml")
	printf '\n-- fence chain (second segment) --\n'
	local uw st2
	uw=$(daemon_cfg use_watchdog)
	st2=$(cat "$WDEV_DIR/state" 2>/dev/null)
	printf 'use_watchdog=%s  device state=%s  device raw timeout=%s\n' \
		"${uw:-?}" "${st2:-?}" "$(cat "$WDEV_DIR/timeout" 2>/dev/null)"
	if [ "${uw:-0}" != 1 ] || [ "${st2:-}" != active ]; then
		printf 'WARNING: the watchdog chain is NOT fully armed -> a takeover can never stop a writer\n'
		verdict=1
	fi
	printf 'io_timeout must be identical on every host (sanlock computes the takeover timeout\n'
	printf 'from the OTHER host\x27s io_timeout, read from that host\x27s lease):\n'
	if ! check_tiers; then
		printf 'WARNING: io_timeout differs between hosts -> the takeover moment would be computed wrong\n'
		verdict=1
	fi
	printf '\nVERDICT: %s\n' "$([ "$verdict" = 0 ] && echo YES || echo NO)"
	exit "$verdict"
}

case "${1:-}" in
disk)    cmd_disk ;;
local)   cmd_local ;;
can-boot) shift; [ $# -ge 1 ] || die "usage: $0 can-boot <vm-uuid|xml-file>"; cmd_can_boot "$1" ;;
*) printf 'usage: %s disk|local|can-boot <vm-uuid|xml-file>\n' "$0" >&2; exit 2 ;;
esac
