# VirtX HA — How It Works

This document explains the high-availability (HA) part of VirtX: what it does,
how the pieces fit together, and why they are built the way they are. It is
written for a software engineer who knows Go and Linux but has never worked on
an HA system. The terms that matter are defined where they are first used, and
listed in the index at the end.

It describes the code in the `virtx-poc-ha` checkout, the HA proof of concept
built on the plain VirtX tree. That tree is complete enough to have been
measured on a real three-host cluster, and section 13 lists what it deliberately
does not do yet.

If you have ten minutes, read §0, then §1 (why silence is not death), §4 (one
round), and §10 (the timing). Everything else can be looked up when a question
brings you to it; the index at the end says where.

**Contents**

0. The system in one paragraph
1. The problem
2. The key design decision
3. The moving parts
4. One round of the system
5. The decision table
6. The gate
7. The coordinator
8. The action: a take-over
9. The isolation response
10. Timing
11. Safety properties
12. Watching it work
13. What is not here
14. Code map
15. Index of terms

---

## 0. The system in one paragraph

Every host writes a short note about itself to the shared storage every five
seconds. Every host reads every other host's note, asks sanlock about the disk
leases, and pings the other hosts' management addresses. From those observations
each host places every peer into one row of a small decision table. A row that
says "this peer's VMs should run somewhere else" becomes a *restart* judgement.
If the action switch is on and one host holds the *coordinator office*, that
host asks a live host to take the VM over; the host that will run the VM moves
its registration, waits until the old owner has released the disks, and starts
it. A host that has published that it is isolated does not wait for the others:
it gives the office up and, after a delay, stops the VMs the cluster has claimed
from it.

Everything that follows unpacks that paragraph.

A **restart** is the decision; a **take-over** is the action that carries it
out (which may move only the registration and boot nothing). The verdict `hold`
means *do nothing and explain*; it has nothing to do with holding a lease.

**Isolation ends in three steps, not one.** It is worth keeping in mind, because
the stop is the middle of a loop rather than the action itself:

1. **The cluster claims the VM first.** The claim moves the VM's registration in
   the shared tree; it does not touch the disk leases, which are a different
   resource.
2. **The isolated host sees the claim** and stops or shuts the VM down; its disk
   leases are released as a consequence.
3. **The landing takes the leases and starts the VM** on the new host.

Two limits come from the same design, and both are deliberate:

- a VM that was **powered off** on the isolated host never appears in its offer —
  the offer lists the VMs it is running — so it is not taken over (8.2);
- a host that is **partitioned** (alive, cannot reach the coordinator, but is able
  to ping the isolation address) has no row that acts on it, so **none** of its
  VMs is taken over; that case is left to an operator (5, 12.1).

### 0.1 The pieces at a glance

```
   host h11                 host h12                 host h13
 ┌────────────┐          ┌────────────┐          ┌────────────┐
 │   virtxd   │          │   virtxd   │          │   virtxd   │
 └─┬───┬───┬──┘          └─┬───┬───┬──┘          └─┬───┬───┬──┘
   │   │   │               │   │   │               │   │   │
   │   │   └──── serf ─────┴───┴───┴───────────────┘   │   │
   │   │         which hosts exist; an address only as a fallback
   │   │
   │   └──── ping ── the other hosts' management addresses
   │                (and, for itself, its judge addresses)
   │
   └──────── shared NFS at /vms ──────── sanlock leases, on the same disks
            /vms/reg/cluster_policy         __VIRTX__COORD   (one host)
            /vms/reg/<host>/hastate         host_id leases   (one per host)
            /vms/reg/<host>/<vm>/...        VM disk leases   (one per VM)
```

Three channels, three different jobs, and the whole design is about never
mixing them up:

- **serf** says which hosts exist and, as a fallback, how to reach one. It never
  decides anything about a VM or a host.
- **ping** says whether a host is reachable from this one right now. It decides
  visibility, never ownership.
- **the shared storage** holds every fact a decision rests on: who owns which
  VM, what each host says about itself, the cluster policy, and sanlock's
  leases.


---

## 1. The problem: failures are not all the same

### 1.1 Three failure modes, two of which look identical from outside

A VM runs on exactly one host and writes to disks on shared NFS storage. When a
host stops answering, there are three very different reasons:

| What happened | Is the host running? | Are its VMs still running? | What the cluster must do |
|---|---|---|---|
| **The host crashed or was powered off** | no | no | start the VMs somewhere else |
| **The host is alive but lost the network** | yes | yes, still running | *not while they run*: two writers on one disk would destroy the VM; once the isolated host has stopped them, they are rescued (see 9) |
| **The host is alive, its network is fine, but it cannot reach the shared storage** | yes | yes, but their disk I/O is failing | hold: an alive host that cannot release its disk leases is not restarted automatically; an operator resolves it (see 5) |

The second case is called **isolation**: the host is cut off, but it is healthy
and its VMs are still writing to their disks. The third is a **storage outage**
on one host: the host keeps running, but it can no longer renew the leases that
prove it is alive, nor the leases its VMs hold on their disks.

The whole HA design exists to tell these cases apart from the outside. The first
two look the same from the network — silence — and that is what makes them hard;
the third still answers, but it cannot renew anything.

### 1.2 Why "it stopped answering, so it is dead" is not good enough

If the cluster only counted silence, it would start the VMs of an isolated host
on a second host while the first host is still running them. Both hosts would
write the same disk images. The guest file system would be corrupted, usually
beyond repair.

So the rules are:

1. Never act on silence alone; act on **evidence**.
2. When two hosts could act on the same VM, only one may be allowed to.
3. When in doubt, do nothing and say so.

Everything else in this document is a consequence of those three rules.

The POC was exercised on three hosts. The rules themselves do not count hosts,
but nothing here has been designed or measured for a two-host cluster.

---

## 2. The key design decision: the shared storage is the witness

The cluster has three ways for hosts to talk: the management network, the serf
gossip membership, and the shared NFS storage at `/vms`. The first two die in
exactly the failure we care about. The shared storage survives that one: a host
that loses the management network can still read and write `/vms` for a while, and the data
written there is visible to every other host. The witness has no redundancy of
its own in this POC: if the NFS server itself fails, every host loses the tree
at once and every row holds. (`/vms` is a hard-mounted NFS
filesystem: while the server is unreachable, an access to it blocks instead of
failing quickly. That is why the loops that read the tree run apart from the
loop that writes this host's report — see 3.2 — and why every wait in this
document is bounded by a clock rather than by "the call returned".)

**serf** is the gossip layer VirtX already has; it tells each daemon which peers
exist. No liveness or ownership decision is taken from it, because serf is exactly the
channel that dies in the failure we care about; its data does reach the landing
choice indirectly, through the inventory (8.1). It is still where a management address is
looked up, and only as a fallback: a decision pings the address the host reports
for itself (`mgmt_addr`) first, and uses serf's member tag only when a host
reports none.

The **peer set** itself comes from the registration tree: one directory per host
under `/vms/reg`. It cannot come from serf, because a host that lost its
management network must still see its peers — otherwise it could never conclude
that it is isolated.

So the shared storage is where the cluster keeps the facts it needs during a
failure:

- a **self report** per host (`/vms/reg/<host-uuid>/hastate`),
- the **registration** of every VM: which host owns it,
- a **claim record** while a VM is being moved,
- the **cluster policy** (`/vms/reg/cluster_policy`),
- and, underneath everything, **sanlock leases** stored on the same shared disks.

> **Rule of thumb:** a fact that records who owns a VM, or that its disks are in
> use, must come from the shared storage; the network probes decide only whether
> we can *see* a host, and they can never overrule the record.

### 2.1 What all of this rests on

The guarantees in this document hold under five assumptions. They are worth
stating once, because every one of them has been seen to fail in the field:

1. **Three hosts.** The POC was exercised on three; the rules themselves do not
   count hosts, and nothing here was designed or measured for two (1.2).
2. **No out-of-band fencing.** Nothing cuts power to a host. Safety comes from
   sanlock's leases, from the host's own watchdog, and from the isolated host
   acting on itself (13).
3. **The watchdog is effective.** A host whose sanlock daemon stops must reset
   within the timeout; if it does not, a host that has "died" can come back and
   write (3.4). The setting that makes it so, and the state directory the daemon
   needs, are in 2.2.
4. **The shared filesystem is the only writer's arbitration for the tree.**
   Ownership is a directory rename, so a stray copy outside this code would make
   ownership ambiguous; the tree is checked for that and a conflict is reported,
   never resolved by guessing (3.1).
5. **Clocks are close enough for the two cross-host comparisons** (the attempt
   deadline and the age of an inventory record). A fast clock can only refuse an
   attempt, never start a second one (7).

### 2.2 What each host must provide first

Two things are true of the host, not of the daemon, and HA does not work without
them. They are what an operator sets up once, before the cluster is armed.

**1. The state directory.** `/var/lib/virtx` must exist and be writable by the
user the daemon runs as (the `qemu` user here, which is why the directory is
owned by it):

```
install -d -o qemu -g qemu -m 0755 /var/lib/virtx
```

The **applied copy** of the policy lives there — `cluster_policy.applied` and
its `.meta` file. It is the only thing that lets a host keep its settings when
the shared tree is unreachable (3.5). If the directory is missing, the daemon
still runs: it warns every round (`could not store the policy for the next
outage`), keeps the policy in memory, and keeps acknowledging the generation, so
nothing looks broken — but a daemon restarted while the shared tree is
unreachable has no copy to fall back on, falls back to the documented defaults,
and the defaults have the switch off. The host then stops acting exactly when it
is most needed. `install -d` is idempotent; running it on an existing directory
changes nothing.

**2. sanlock's watchdog and tier.** `/etc/sanlock/sanlock.conf` on every host:

```
use_watchdog = 1
io_timeout = 10
watchdog_fire_timeout = 60
```

The whole death arithmetic in this document is built from those two numbers:
`death threshold = 8 x io_timeout + watchdog_fire_timeout` (140 s at the 10/60
pair), the attempt window `= 30 s + death threshold + 10 s` (180 s), and every
timing in section 10. virtxd reads the pair from sanlock at startup and derives
its own windows from it, so hosts on different tiers would disagree about when a
peer is dead.

`use_watchdog = 1` is the part that makes a host that hangs stop being a host:
without it, sanlock never arms the host's watchdog, a hung host is never reset,
and it can still be writing when another host has taken its VMs over. Its lease
still expires, the row still reads DEAD, and the cluster would still take the VM
over — the difference is whether the old host can come back. That is exactly the
assumption §2.1 states, and this is the setting that makes it true.

Changing either sanlock setting means a sanlock restart, or a reboot of the
host, before virtxd is started again; the state directory can be created at any
time, and the applied copy appears in it on the next round. On a host whose
sanlock is already running, do **not** stop it to change these values: write the
file and reboot the host instead. Stopping the daemon leaves the watchdog armed
and unpeted, and that resets the host.

---

## 3. The moving parts

### 3.1 The shared registration tree

```
/vms/reg/
├── cluster_policy                 one file, all cluster-wide settings
├── cluster_policy.request         an operator's request to change the policy
├── <host-uuid>/
│   ├── hastate                    this host's self report (JSON)
│   ├── vminfo                     the VMs this host publishes as running here
│   ├── lockid                     the sanlock id other hosts know this host by
│   └── <vm-uuid>/
│       ├── <vm-uuid>.xml          the libvirt domain definition
│       ├── claim                  written by a landing host during a take-over
│       └── <op>.log, <op>.msg     the per-VM operation log
└── ...
```

Ownership is the **directory nesting**: a VM belongs to the host whose
`<host-uuid>/` directory contains it. Moving a VM to another host is renaming
that directory. A rename is the only way the tree changes hands, so ownership is
either the old directory or the new one, never both — but only if nothing
outside this code leaves a copy behind. The tree is therefore checked for a VM
that appears under two hosts, and such a VM is reported as a **conflict, not a
winner**: the snapshot leaves it out of both hosts' views and the isolation
response keeps it running, because a VM nobody can be shown to own is acted on
by nobody.

**Registration** in this document is that directory. sanlock has an unrelated
word that sounds the same — a **lease registration** — for its own bookkeeping
of a lease it handed out (7.4).

The report, the policy, the domain XML and the `vminfo` are written atomically
(a temporary file, then a rename), so a reader never sees half of one: it sees
the old content or the new one. Atomic here means "one file, two states"; it
does not by itself protect against the NFS server losing data it had not yet
committed, and no fsync is made. The claim record and the operation log are
plain writes; the claim is only a marker, and the registration move — the
rename — is the authoritative act.

### 3.2 Self reports (`hastate`)

Once every `ha_hastate_refresh_seconds` (default **5 s**), each host writes a
small JSON object about itself:

| Field | Meaning |
|---|---|
| `uuid`, `boot` | who this is, and when it booted |
| `ts`, `heartbeat` | when the report was made, and the host's uptime in seconds |
| `probed`, `judges_silent` | whether the probe ran, and whether any judge address answered |
| `isolated` | the conclusion: judges silent **and** the cluster is lost (an office holder is the one exception, 9.1) |
| `office_epoch` | the coordinator epoch this host holds, 0 if none |
| `mgmt_addr` | the address other hosts should use to reach it |
| `policy_gen` | the cluster-policy generation this host has applied |
| `isolation_running_vms` | the VMs this host is still running while isolated |
| `reason` | a short word for the operating picture |

Two rules make reports trustworthy:

- **Freshness is judged by watching the report change.** A host keeps a
  fingerprint of four fields (`ts`, `heartbeat`, `isolated`, `probed`) for every
  peer. When the fingerprint changes, the window reopens. The peer's `ts` is
  never compared with the reader's clock: a clock that lags would make a peer
  look stale right after it wrote, and one that runs ahead would make it look
  fresh forever.
- **A report that stands still for longer than `ha_hastate_stale_seconds`
  (default 30 s) stops being evidence.** It does not by itself make the host
  dead; it is one input of the `Failed` row, which also needs a DEAD host_id
  lease and an unanswered management address (see 5).

The report is classified into five states: `fresh-0` (fresh, not isolated),
`fresh-1` (fresh, says it is isolated), `stale`, `unreadable`, `absent`.

The observation loop runs in its own goroutine because every read goes to the
NFS mount, which is mounted hard: while the server is unreachable, a read blocks
instead of failing fast. Separate goroutines keep a slow read from delaying the
report loop, but they cannot make the write succeed through a hung mount: if
/vms is gone for everyone, every host's report blocks too, and the cluster as a
whole holds still. That is the fail-closed direction, not a liveness trick.

### 3.3 Judge addresses, and the isolation probe

"Is my own network broken?" cannot be answered by asking the cluster, because
the cluster is what is unreachable. So each host probes one or more **judge
addresses** that are not cluster members: by default the gateway of its own
management network, or up to ten addresses configured in
`ha_isolation_address_0 .. _9` — cluster-policy settings, like everything else
in section 10.

If no judge answers, the host treats its own network path as broken. A judge
that is down looks exactly like a broken path, so the judge set must sit outside
the cluster but on the same path as the management network. A down judge can
therefore only ever be *half* an isolation: section 9.1 pairs it with "and the
cluster is gone", and a false half never stops anything.

### 3.4 sanlock leases

**sanlock** is a small daemon that hands out **leases** stored on the shared
disks. A lease is a record on disk that its holder must keep renewing; if the
holder dies, the lease expires. The daemon can list every lease at once; that
list is the **lease table**, and one entry of it is a **lease record**
`{name, timestamp, owner, generation}`. A record whose timestamp is zero is
**free**: no live holder has it.

Three different leases matter here:

| Lease | Who holds it | What it proves |
|---|---|---|
| **host_id lease** | each host's sanlock daemon | this host is alive and renewing |
| **VM disk leases** | libvirt, on behalf of a running VM | these disks are claimed by a writer |
| **coordinator lease** (`__VIRTX__COORD`) | one host, on behalf of the cluster | this host is the coordinator (see 7) |

The set of leases shared by a cluster is its **lockspace**. Inside its lease
record the holder may write a **lock-value block**: a small fixed-size field of
the resource's on-disk record (sanlock reserves one 512-byte block for it) that
other hosts can read. The coordinator uses it to publish two numbers (see 7).

The death of a host is not guessed: **sanlock** — not this code — decides that a
peer is DEAD, and it does so only after that peer's host_id lease has gone
unrenewed long enough. sanlock's **tier** is two settings: `io_timeout = 10 s`
and `watchdog_fire_timeout = 60 s`; this document says "the 10/60 tier" for that
pair. sanlock gives a host eight missed renewals (8 × `io_timeout`) before it
stops renewing the host's lease, and then waits one more `watchdog_fire_timeout`
before anyone may read the peer as DEAD. The code does not invent these two settings: it reads them from sanlock at
startup. It does name the multipliers itself — eight missed renewals and two
for the renewal window are Go constants — and it derives its own windows from
them:

```
death threshold = 8 x io_timeout + watchdog_fire_timeout  = 8 x 10 s + 60 s = 140 s
renewal window  = 2 x io_timeout                          = 20 s
```

The **watchdog** is a hardware timer on the host that the sanlock daemon keeps
petting; if the daemon stops petting it — because it hangs or dies — the timer
fires and resets the host. That is what turns a hung host into a dead one, and
it is why a host that has crashed cannot keep writing. A daemon that is still
alive but whose renewals fail (a host that lost its path to the storage) keeps
petting the timer: its leases expire, but the host is not reset. That is the
difference between the two "the host may be alive" rows and `Failed`.

**What a lease record does when its holder dies.** A lease is free only when its
record says so: its timestamp is zero, which only a release writes. A host that
died while running a VM leaves its disk-lease record with a non-zero timestamp,
so a reader still sees "held" — and that does not contradict "a held lease means
a live writer": what changed is that the old holder's host_id is now DEAD, and
sanlock then lets another host take the disk lease. The landing's wait (8.4)
runs out, and the boot asks for the lease and gets it; after the death, "held"
is a record waiting to be taken, not a writer.

That also fixes what "free" means for the rest of this document: every
disk-lease record of that VM has a zero timestamp. The `Failed` row does not
gate on the lease read at all (8.3). The two rows whose owner may still be alive
do: they restart only when the records read free. A host that cannot reach the
storage cannot write a release either, so those rows hold and an operator
resolves them — deliberately: no evidence, no action.

In the code, the read is `Lease_state` in `pkg/verdict/verdict.go`; the
acquisition itself is sanlock's, driven through `pkg/lockman/hosts.go`.

The VM disk leases are the last line of defence for the data. A lease is a
record only one holder can own: a second host cannot take it while the first
keeps renewing it. So a start that still races a live writer fails on qemu's
image write lock instead of writing. The two locks are different: sanlock's
**lease** lives on the shared storage and the landing *waits* for it (8.4); the
qemu **image write lock** is a lock on the disk image file itself, held by the
qemu process until that process exits — which is why a start can still fail
right after the lease goes free (7.2). Over NFS that lock is enforced by the
server's lock manager, so it is only as strong as the NFS server; this document
does not claim more. This tree does not configure libvirt to pause such a boot;
it fails, and that failed boot is what the retry ladder is for.

At startup the daemon reads the effective tier from sanlock, prints where each
of the two settings came from, and checks that every window the code uses is
still the one its own derivation produces. That check cannot fail in a clean
build, because both sides call the same function; it exists to catch a change
that stops deriving a window and hard-codes one instead.

### 3.5 The cluster policy

The settings every host must agree on live in one file on the shared tree:
`/vms/reg/cluster_policy`. The file carries a **generation** that only ever
moves forward, and a host refuses a policy older than the one it already
applied: a host that comes back with an old view must not drag the cluster back.

A host reads the file every round. A line it cannot use only adds a warning and
keeps the value it had; unknown settings are ignored and a duplicate warns. The
rest is a fallback chain:

- if the file can be read, its settings are used and kept as this host's
  **applied copy** in `/var/lib/virtx` (with a hash and the generation);
- if it cannot be read right now, the host keeps running with the applied copy,
  which is how a host keeps working while the shared tree is out of reach;
- if there is no applied copy either, the documented defaults apply.

A file that is *missing* is treated as "removed" only after three rounds in a
row, so a momentary miss does not disarm the cluster.

The most important setting is the switch, `ha_enabled`:

- **off (the default):** the cluster only judges and logs. This is shadow mode:
  useful, but nothing is ever moved.
- **on:** a `restart` judgement may be executed.

The switch is the exception to the forgiving parse: it can only become true
through a well-formed line in that file, and no per-host configuration file can
arm the cluster. With no readable policy at all, the switch is off. Either way,
a host without the shared tree cannot act for long: the coordinator lease lives
there too, so a host that cannot take the lease holds no office.

The other settings are in section 10.

### 3.6 Turning the switch on

`virtx enable ha` does not write the policy. It records a **request** in
`/vms/reg/cluster_policy.request`, and the command returns at once. Whichever
host holds the coordinator office then serves it, writes the policy under a
generation one above both the file it read and its own applied copy, and renders
every documented setting into the file. (Section 7 explains when an office
exists at all; with the switch off, a host competes for it because a change is
pending.)

Every host that is publishing a report acknowledges the change by carrying that
generation in its report as `policy_gen`. The acknowledgement is read from the
*report*, not from the applied copy: a host whose daemon stops loses its
acknowledgement, and the command reports the change as pending again even though
that host applied it long ago. So a change is not a broadcast: it is
a state the hosts converge on. The command then waits a few seconds for those
acknowledgements (`--timeout`, 3 s by default; `--no-wait` returns at once) and
names the hosts that have not applied it yet. Running out of time is not a
failure: the file is the authority, and the cluster converges on it on later
rounds. `virtx get ha` prints the same progress.

`virtx disable ha` also clears the hosts' self reports once everybody has
applied the change. Those reports are the only place the cluster keeps the
*acknowledgement* of a change (the policy file and the applied copy hold the
generation itself), and they are rebuilt by the hosts within one round; the
switch is off at that point, so no row can act while they are missing.

The command only changes the switch. Every other setting is a line in the same
file: the writer renders the whole policy it read, with the switch set as asked,
so a setting an operator changed in the file survives the next request. The POC
has no command for the other settings; changing one means editing the file and
moving the generation forward (a lower generation is refused, 3.5).

---

## 4. One round of the system

A **round** is one pass of the loops: one refresh period, five seconds by
default. Everything that says "this round" or "two rounds" counts those passes.

Every host does the same things once per period. They are not one loop in the
code; there are several goroutines with the same period:

| Goroutine | What it does |
|---|---|
| report loop | probe the judges, write this host's report, run the isolation response |
| observation loop | read the policy, every peer's report, the lease table |
| judgement loop | ping management addresses, select a row for every peer, apply the gate, call the action half |
| coordinator loop | hold or take the office, settle attempts in flight, publish the attempt deadline |

The causal order of a round is:

```
 1. observe:  policy, peers' reports, lease table
 2. judge:    ping management addresses, one row per peer, then a verdict,
              logged when it changes
 3. gate:     may this verdict become an action?  (section 6)
 4. act:      the judging host calls the action half; only the office holder
              executes, and it watches the attempt it starts
 5. send:     the office holder sends the take-over instruction to a landing host
```

Step 4 has no hand-off and no queue: each host applies the gate to its own
judgement and calls the same action function; a host that does not hold the
office logs `action=not-coordinator` and drops the round. Convergence comes from
every host judging every peer, not from one host being told to act.

There is no shared decision log, so the judgements never meet: the office holder
acts on **its own** rows. A holder that misreads one peer — an unreadable lease,
a failed ping — can hold a take-over that another host would have judged. That
is a liveness cost, not a safety one, and it is why the operator's `get ha`
shows the *local* host's view rather than the cluster's.

---

## 5. The decision table

Every peer is classified from three observations:

| Observation | Question it answers | Source |
|---|---|---|
| **Report state** | what does its self report say: `fresh-0`, `fresh-1`, `stale`, `unreadable`, `absent`? | shared storage |
| **Host lease** | is that host's own sanlock host_id lease LIVE, DEAD, or unreadable? | sanlock |
| **Management** | is its management address known, could we ask it, and did it answer? | ping |

The three are combined by fixed rules. They are tried top to bottom and the
first match wins. In the table, `—` means "don't care", and `fresh-*` means
`fresh-0` or `fresh-1`.

| Host lease | Report state | Management | Row |
|---|---|---|---|
| unreadable | — | — | `StorageUnreadable` |
| DEAD | `fresh-*` | — | `HostAliveStorageLost` |
| DEAD | `stale` / `absent` / `unreadable` | answers | `HostAliveStorageLost` |
| DEAD | `unreadable` | does not answer, unknown, or not asked | `PartitionStorageUnreadable` |
| DEAD | `stale` / `absent` | known, asked, does not answer | `Failed` |
| DEAD | `stale` / `absent` | unknown, or not asked | `NoRow` |
| not DEAD | `fresh-*` | unknown, or not asked | `NoRow` |
| not DEAD | `fresh-1` | asked, does not answer | `Isolated` |
| not DEAD | `fresh-0` | asked, does not answer | `Partitioned` |
| not DEAD | `stale` / `absent` / `unreadable` | asked, does not answer | `NoRow` |
| not DEAD | `fresh-*` | answers | `Running` |
| not DEAD | `stale` | answers | `AgentDown` |
| not DEAD | `unreadable` / `absent` | answers | `NoRow` |

"Not DEAD" means the host_id lease could be read and did not say DEAD: it is
LIVE, or one of the three states the table does not trust — a failed renewal
(`FAIL`), a reading too old to be trusted (`UNKNOWN_OLD`), or free (no holder at
all: the lease was given back rather than lost, so it is not evidence that the
host died). Any combination not
listed is `NoRow`.

The two names that look alike are worth telling apart: `StorageUnreadable` means
we could not read that host's **host_id lease** at all; `PartitionStorageUnreadable`
means the host_id lease did read, and said DEAD, but the host does not answer and
its **self report** cannot be read either. The first knows nothing; the second
knows the host is dead and cannot check why.

Each row maps to a verdict. The three words you will see in the log and on the
console line up like this — `restart` becomes an `action`, `hold` stays `hold`,
and `alert-only` becomes `alert`:

| Row | Meaning | Verdict |
|---|---|---|
| `Running` | everything is normal | `no-op` |
| `Failed` | the host is gone: no report, no answer, host_id lease DEAD | `restart` |
| `AgentDown` | the host is alive and answers, but its report stopped being written (its agent, virtxd, is wedged) | `alert-only` |
| `Isolated` | alive, cannot reach its judges, and cannot reach the cluster | `restart` |
| `Partitioned` | alive, cannot reach us, but its own report says it is not isolated | `hold` |
| `HostAliveStorageLost` | it answers or still reports, but its host_id lease is DEAD (it lost the storage) — the two can coexist: a host that can still write its report while its lease is past the threshold, for example one that just came back before re-acquiring it | `restart` **only if** every disk lease of its VMs is free |
| `PartitionStorageUnreadable` | its host_id lease is DEAD, it does not answer, and its report cannot be read | `restart` **only if** every disk lease of its VMs is free |
| `StorageUnreadable` | its host_id lease state cannot be read at all | `hold` |
| `NoRow` | the observations establish nothing actionable — including "we could not ask", and the window before a dead host's lease reads DEAD | `hold` |

Four ideas are worth spelling out:

- **`NoRow` is not an edge case.** A host that has just been powered off is
  `not DEAD`, its report goes `stale`, and our ping fails: that is `NoRow` and
  `hold`. Nothing may happen yet, and that is the point of the death threshold:
  sanlock needs eight missed renewals plus the watchdog timeout before anyone
  may read DEAD.
- **"No evidence" is not "evidence of absence".** A lease record that cannot be
  read does not say a VM is still running, and it does not say it is not. Careful
  here: a **host_id lease** that cannot be read is `StorageUnreadable`; an
  unreadable **disk-lease table** is a different failure, and it makes the two
  rows whose owner may still be alive (`HostAliveStorageLost`,
  `PartitionStorageUnreadable`) hold.
- **Only positive evidence restarts a VM.** A row that rests on the disk leases
  restarts only when they say every VM of that host is free. The `Isolated` row
  does *not* wait for the leases to be free, because an isolated host is alive
  and holds them on purpose until it sees the claim (section 9). What protects
  the VM there is the ordering, not the leases.
- **No registrations, no answer.** When a claim has already moved a VM's
  registration away from that host, there is nothing left to read the disk
  leases from: the answer is "unknown", not "free", and the two rows that need
  that reading hold. The rows that do not need it (`Failed`, `Isolated`) are
  unaffected: a host is only ever restarted on a reading it actually took.

A host that lost only the storage path still answers for a while, but its VMs
cannot renew their disk leases either. The row asks for a restart, and the gate
then asks for every one of those leases to read free — but a host that cannot
reach the storage cannot write a release either, so in practice the row holds
and an operator resolves it. It restarts only when the records really read free,
which is what a host that came back and released them looks like.


### 5.1 What you will see, per row

The row is the internal classification; the console and the log translate it.
`virtx get ha` prints one line per judgement that is not `no-op`, with the first
word taken from the verdict (`action` for restart, `alert` for alert-only,
`hold` otherwise). The log prints the same judgement as fields, and it adds the
gate's reason when there is one: `armed-off` (the switch is off), `no-evidence`
(the row's lease evidence is missing) or `t-takeover` (the reset margin has not
been served). A missing second confirmation is not a gate reason: the judgement
loop has already turned the verdict into `hold` before the gate is asked, so the
log shows `verdict=hold` and the console shows a hold line.

| Row | Verdict | What `get ha` prints | What the log says |
|---|---|---|---|
| `Running` | `no-op` | nothing (this is the healthy state) | `row=Running verdict=no-op`, on change |
| `Failed` | `restart` | begins `action`, then `h13: Failed` | `row=Failed verdict=restart ... action=armed-off` and, once the gate passes, the attempt's result |
| `AgentDown` | `alert-only` | begins `alert`, then `h13: AgentDown` | `row=AgentDown verdict=alert-only` |
| `Isolated` | `restart` | begins `action`, then `h13: Isolated` | `row=Isolated verdict=restart ...` plus the attempt |
| `Partitioned` | `hold` | begins `hold`, then `h13: Partitioned` | `row=Partitioned verdict=hold` |
| `HostAliveStorageLost` | `restart` or `hold` | begins `action` when the leases read free, otherwise `hold` | `row=HostAliveStorageLost verdict=...` |
| `PartitionStorageUnreadable` | `restart` or `hold` | same rule | same rule |
| `StorageUnreadable` | `hold` | begins `hold`, then `h13: StorageUnreadable - its lease record could not be read: nothing is known about it, nothing is done` | `row=StorageUnreadable verdict=hold` |
| `NoRow` | `hold` | begins `hold`, then `h13: NoRow` | `row=NoRow verdict=hold` |

Two cautions. The console prints `action` for a `restart` verdict as soon as the
row is a restart, before the gate has decided anything: "the console says
action" is not the same as "the cluster acted", and the log line is what
decides. And while the second confirmation is still missing, the verdict itself
is `hold`, so the console shows `hold h13: Failed` and the log shows
`verdict=hold`; the `action` word appears only after the confirmation lands.

One case is worth naming separately because it is deliberately quiet: a host
that is **shut down cleanly** releases its host_id lease on the way out, so the
record reads free rather than lost, the row is not `Failed`, and nothing is
taken over. That is correct — the VMs were stopped with the host. Only a lost
lease, not a released one, is evidence that a host died.

A healthy cluster produces `no-op` rows and stays quiet: only changes are
logged.

---

## 6. The gate: from judgement to action

A `restart` verdict does not act by itself. It must pass the gate:

1. **Only `restart` is actionable.** Everything else is logged.
2. **The action switch must be on.** This is `ha_enabled`, resolved every round
   (section 3.5). If the switch is off, a restart verdict is a log line and
   nothing else.
3. **The row's own evidence must be complete.**
   - Every row that rests on the host_id lease reading DEAD (`Failed`,
     `HostAliveStorageLost`, `PartitionStorageUnreadable`) needs the **second
     confirmation**: two DEAD readings at least one renewal window apart, with
     no live reading in between. A single reading can be an artefact of the
     daemon's own status — a re-read after a restart, a status that has not
     caught up — so the confirmation asks that the state survived a whole
     renewal period. The judgement loop applies it before the gate, and
     downgrades the verdict to `hold` when it is missing.
   - `HostAliveStorageLost` and `PartitionStorageUnreadable` additionally need
     every disk lease of that host to be free. `Failed` and `Isolated` do not
     (section 5).
4. **A `Failed` host must have waited the reset margin** (15 s) since it was first
   read DEAD. That margin is the time the dead host's storage paths need to be
   reset. A measurement recorded in the code (2026-09-30) put it at about 4 s;
   treat that as historical, and 15 s as room for a slow one. The clock starts at our first DEAD reading,
   never at the moment the host died, because nobody can observe that moment.

If all four pass, the verdict is handed to the **action half** (the code calls it
*the drive*), the coordinator's half of the round (section 7).

---

## 7. The coordinator: why only one host may act

Every host judges. If every host acted, two of them could pick up the same VM.

The right to act is a sanlock lease of its own, the **coordinator lease**
(`__VIRTX__COORD`) in the shared lockspace. Whoever holds that lease is the
coordinator. There is no election and no voting: it is a first-come lease, and
the winner is unique because the shared storage makes it so.

The word **handover window** in this document always means the same number:
the attempt window (180 s at the 10/60 tier). After a host first observes the
coordinator's host_id as unreachable, it waits that long before taking the lease
(the lease is still the storage's; the wait is what makes "the holder is really
gone" true). The clock starts at the first round in which that lease is seen
unreachable — a reading, not the death — so the wait is measured on the same
observer's clock as every other window here. The **detection term** (30 s) is one of the three parts of that
180 s — how long the detecting host needs to be sure the old holder stopped
renewing — not a second window. A host that resigned keeps itself out of the
contest for the same window, so it cannot immediately take the office back.

A host wants the office when **the switch is on**, or when **a change of the
switch is pending**. So:

- switch off, no pending change: the cluster holds no coordinator lease at all;
- switch on: some host holds the office, even when nothing is failing, because
  that host is also the one that serves a policy request;
- the office moves when its holder dies, resigns, or finds itself isolated
  (section 9.3), or when an operator asks for a named host to hold it (7.5).

While it holds the office, the coordinator publishes two numbers in the lease's
**lock-value block**:

- the **epoch** (a counter kept in the lease, bumped each time the office is
  taken — a fencing token: an instruction from a deposed coordinator carries an
  older epoch and is refused; because it is stored, it survives restarts and can
  be large on a cluster that has taken the office many times),
- the **attempt deadline** (below).

**The attempt deadline** is the deadline of *one attempt*: it is published before
the instruction goes out, re-checked by the landing host, and it
is the end of the idempotency key's window. It lasts one attempt window (180 s at
the 10/60 tier). It is not a lock that stops anyone from deciding: the only
things it does are to make a late instruction stale and to end the key's
memory.

The duplicate barriers are elsewhere:

- an instruction that carries an older epoch is refused (the log word is
  `stale-epoch`, 8.3);
- a host cannot claim a VM whose registration has already moved;
- a duplicate inside one window is refused by the landing's key memory
  (`already-running` / `duplicate`);
- an instruction whose epoch cannot be read at all is *not* refused for its
  epoch: the lock-value block is where the epoch lives, and an undecodable block
  leaves it unknown (13).

One deliberate compromise comes with the deadline: it is one of two places where
a host compares another host's clock (the other is the age of the inventory
record in the landing rule). A clock that runs ahead can cost the attempt: the
landing refuses it, and a refusal is not retried inside the epoch
(7.2), so the rescue waits for the next epoch. The safety direction is unchanged
— a fast clock can only refuse an attempt, never start a second one — and
`stale-epoch` is the guard against an old coordinator, not against a fast
clock.

### 7.1 The idempotency key

Each attempt has a key:

```
key = (epoch, victim host, vm uuid, attempt number)
```

The **victim** is the host the VM is being taken from — dead or isolated.

The key exists because "send the instruction" and "do the work" are not atomic.
If the coordinator retries after a timeout, the landing host must be able to
tell a retry from a second job. The landing remembers the keys it has executed
for one attempt window; a key seen again inside that window is refused instead of
being executed twice.

Worked example: epoch 7, victim `h11`, VM `1234`. Attempt 1 is issued and
accepted; its window is open for the next 180 s. A duplicate of that instruction
that arrives while the window is open is refused by the landing's key memory
(`duplicate`, or `already-running` when the VM already runs there). If the
attempt is still unconfirmed when the window closes, it is recorded as
`timed-out` and the coordinator issues attempt 2 — a different key. A copy of
attempt 1 that turns up after the close is refused by its own deadline. Either way nothing is executed twice. The victim is in the key
because the same VM can legitimately be claimed from two different victims
inside one epoch; without it, the second claim would have been answered "this key
already landed" forever.

### 7.2 Retries are new attempt numbers, never repeats

A take-over can fail once and succeed a moment later: the VM was just stopped on
the isolated host, and the start fails on the disk lock that the stopped qemu
has not yet released. So a timed-out attempt is tried again as the **next
attempt number** (1 → 2 → 3, three attempts at most), and a new attempt number
is a new key. Nothing that was executed is ever sent again. Other outcomes are not
retried: an attempt still in flight is waited for, and a refusal or a landed
take-over stands. A boot that fails in its first second is not told apart from
one that is merely slow: the attempt reports `timed-out` when its window closes,
and that is what feeds the ladder.

### 7.3 What the coordinator does in one round

1. Take the candidates: every VM of the victim that the row says may be taken
   over. For an isolated victim, only the VMs that victim offered in its report.
2. Pick a landing host (section 8.1), or leave the VM pending with an alert if
   no candidate exists. There is no preemption: nothing is stopped to make room.
3. Build the key, remember it for this epoch, issue the instruction to the
   landing host over the REST API.
4. Watch the landing confirmation until the attempt window runs out, and record the
   outcome.

That memory is in RAM only. A new coordinator inherits nothing and re-judges:
a successor inheriting a half-done intention would finish it twice.

### 7.4 Taking the office after a crash

A killed holder leaves a stale **lease registration** — sanlock's own bookkeeping
for a lease it handed out, unrelated to the VM registration of section 3.1 — and
sanlock then refuses to register the same resource again until it is given back.
At startup, after joining the lockspace, a host therefore releases its **own**
orphaned `__VIRTX__*` lease registrations: only orphans, and only when the lease
record no longer says the lease is ours (the timestamp is zero, or the owner is
another host). A record that still names this host with a non-zero timestamp is
left alone: that lease is ours and held, so there is nothing to give back. If
the record cannot be read, nothing is given back either: the fail-closed answer
is to wait. Without this sweep, a host that came back could
never compete for the office again.

### 7.5 Moving the office on purpose

The office is not assigned: whoever takes the lease first gets it. That makes a
failure simple, and it is arbitrary for maintenance — an operator who wants to
take the current holder down cannot say where the office should land.
`virtx set ha --coordinator <host>` asks for a named host to hold it:

```
virtx set ha --coordinator h12
```

The command writes a request into the shared registration tree
(`/vms/reg/coord_switch`), the same way a change of the switch is asked for
(3.6). Any host may receive it: the current holder is what serves it. The
request carries the target host, who asked, when, and a deadline 30 s ahead.

The holder serves it by resigning (`Resign`, 9.3): it gives the lease back, and
the handover window keeps it out of the contest afterwards (7). A change of the
policy that is still pending is written before the resign, so a move never
loses an enable or a disable that was already on its way (3.6). The named host
takes the lease on its next round, the epoch goes up by one like any other take
(7), and the request is removed.

The other hosts are what make this a move and not a race. While a live request
names another host, they do not compete for the office even when the lease is
free — otherwise the office would go to whoever was quicker. They wait until
the named host has taken it (the request is gone) or the deadline has passed
(30 s). A named host that never comes therefore costs a bounded wait and
nothing else: after 30 s the others compete as usual. The request says who
should try first; it never says who *may* hold the office, and the lease is
still the only authority.

Two things are checked before anything is written:

- the switch has to be on (`virtx enable ha`). A change of the switch that
  turns it on and is still being acknowledged counts as on, because that
  change is what the office is for — enabling HA and moving the office can be
  done one after the other. A cluster that is being turned off refuses the
  move.
- the named host has to be one the cluster knows (a name from `virtx list
  host`, or its uuid); anything else is refused with that answer.

The command waits 15 s for the office to move and prints the state it found.
Running out of that wait is not a failure of the request — it is on the shared
tree and is served on a later round — so the timeout prints `NOT CONFIRMED` and
the command exits 0, the way `enable ha` reports a change that has not been
acknowledged yet (3.6). `--timeout` extends the wait; `--no-wait` returns at
once.

---

## 8. The action: a take-over

This section answers three questions in order: who runs the VM afterwards (8.1),
whether it should be started at all (8.2), and how it is moved safely (8.3–8.4).

### 8.1 The landing rule

The landing host is chosen from the hosts that are alive this round. The filters
are (the code marks which are implemented):

| Filter | State |
|---|---|
| not the victim | implemented |
| its row this round is `Running` | implemented |
| the inventory's record of that host is fresh (at most 30 s old; no timestamp counts as stale) | implemented |
| a capacity check (does that host have room?) | **not calibrated**: the code passes everybody by a constant |
| not disabled / in maintenance | **not implemented**; passes everybody |

The survivors are ordered by **expected placement** (a stored preference for the
host a VM is normally placed on), then free memory, then uuid. Expected placement
is **not wired yet** — the preference is always empty — so in practice the order
starts with free memory and uuid. The coordinator may itself be the landing
host; it has no privilege in this choice. The concurrency limit
(`ha_max_inflight`, default 1) is not a filter: it is applied when the round's
attempts are planned.

### 8.2 Should this VM start at all?

There are two kinds of take-over:

| In the automatic path | VM state when its owner was last seen | What happens |
|---|---|---|
| owner dead | running | the VM is started at the landing host |
| owner dead | shut off | only the registration moves; the VM is **not** started |
| owner isolated (a rescue) | any | the VM **is** started |

Whether the VM "was running" is decided from two sources:

- the cluster's own record of the VM's runstate, trusted only when the inventory
  places that VM at the victim, and
- the VM's disk lease state (a held lease means a writer claimed the
  disks — not necessarily one that is still alive).

The rule is: **a start needs at least one source to say "running", and neither
source may say "shut off".** When the two disagree, the "shut off" side wins,
because a start is the irreversible act:

- a record that says running with every lease free means it was shut off
  (the lease vetoes);
- a held lease with a record that says shut off means it was also shut off
  (the record vetoes: a held lease may be left behind by a qemu that was
  hard-killed);
- a source that cannot be read is not a disagreement: the other one decides;
- anything that cannot be settled is "unknown", and **unknown means
  register-only** — except on the rescue path, where a VM from an isolated
  victim is started regardless (8.2's table).

The coordinator computes the same answer before issuing, so the two sides
normally agree; the landing's answer is what counts. In the code the decision is
`Takeover_state` in `pkg/coord/landing.go`, and `pkg/virtx/vm_takeover.go` reads
it back with `takeover_state_of` / `takeover_starts`.

The middle rule exists because a VM that was shut off when its owner died was
shut off *on purpose*; nobody decided it should run. The rescue rule exists
because an isolated host stops its VMs *as its isolation response*: the only
reason they are off is that they are meant to run again elsewhere.

### 8.3 The landing's checks

The landing host first decides where the VM is coming from (step 1 of 8.4). It
can refuse:

| Refusal | Meaning |
|---|---|
| `not-registered-there` | the registration is not where the victim says it is |
| the victim is still active | only on the hand-made path: the landing answers 422 "the owning host is still active" because the inventory still shows that host as running |
| `unknown-uuid`, `no-victim`, `victim-is-me` | the instruction names a VM or a victim that does not make sense |

Then it re-checks the instruction itself:

| Refusal | Meaning |
|---|---|
| `stale-epoch` | the instruction comes from an older epoch |
| `stale-mask` | the attempt's deadline is over (`mask` is the code's name for it) |
| `third-party-holds` | a disk lease is held by a host other than the victim (the landing's own hold is not excluded today, so it would be refused too) |
| `gate-held` | the lease evidence for this row says "held", or the lease state could not be read |
| `already-running` | this VM already runs here |
| `no-forward-evidence` | reserved for a lease state that is neither free nor held; currently unreachable (an unreadable state is refused as `gate-held`) |
| `capacity` | not enough room; vacuously true today (the capacity check is uncalibrated) |

The lease-side pair (`gate-held`, `no-forward-evidence`) binds only for the
alive-host rows: `Failed` and `Isolated` skip it. The victim's own hold is not a
third party; the landing's own hold is not excluded today, so it would be
refused as `third-party-holds`.

An instruction for a VM whose registration already sits at the landing is
accepted without a second move (the landing answers 204): that is the "second
half of a claim that was made earlier" case.

### 8.4 Who does what, and in what order

The coordinator only issues the instruction (`POST /vms/{uuid}/takeover`). The
**landing host** does the work, in one request that it answers with 202:

1. decide where the VM is coming from and that the instruction is admissible
   (8.3),
2. move the registration directory to itself,
3. write the **claim record** naming the host the VM came from,
4. fill the VM's record in the inventory as "powered off" — the boot refuses a
   VM the inventory does not know yet,
5. define the domain,
6. decide whether it starts at all (8.2): a register-only take-over stops here
   and leaves the claim behind,
7. wait for the VM's disk leases to be free and boot it,
8. clear the claim record.

The claim record marks a rescue in flight at the landing. It is cleared when the
VM boots here; a register-only take-over leaves it behind, and a landing that
dies between the move and the boot leaves the VM registered at that landing and
shut off. Neither a later round nor a different landing finishes such a VM
automatically — a different landing is refused `not-registered-there` — so it
takes a hand-made take-over. The coordinator reads its own claims when it builds
the candidate list for an isolated victim (9.2), which is how an interrupted
rescue is picked up again.

For an isolated victim the order is the point:

```
coordinator                     landing                      victim (isolated)
    |                              |                              |
    |-- POST takeover ------------>|                              |
    |                              |-- move the registration      |
    |                              |   (in the shared tree) ----->|  sees it
    |                              |-- write claim(from victim)   |
    |                              |                              |-- the same host
    |                              |                              |   twice in a row:
    |                              |<------ stops the VM ---------|
    |                              |                              |   (leases go)
    |                              |-- waits for free leases      |
    |                              |-- boots the VM               |
    |                              |-- clears the claim           |
```

Two details make this robust:

- **The claim record** lives next to the VM's registration, so it survives a
  change of coordinator: a new coordinator that re-issues the instruction leaves
  the same claim where it is.
- **The start waits for the leases.** Before booting, the landing host waits up
  to 90 s (the lease wait) for every disk lease of that VM to have no holder,
  and boots anyway when the wait runs out: a boot into a still-held lock fails
  without writing, and that failure is what the retry ladder (7.2) is for. The
  victim needs a couple of rounds to see the claim and stop the VM, which is why
  the wait is there at all. For a VM that was running on a dead host the lease
  record stays held until the boot takes it, so the wait normally runs its full
  90 s (section 10). The wait is the *hope* that the old holder lets go on its
  own; the protection is that the boot cannot take a lease another live host
  still renews (3.4).
  The **lease keys** — the resource names sanlock uses for that VM's disks — are
  read from the XML of the landing's own copy of the VM (the registration it has
  just moved), not from the victim's. When they cannot be read at all, the landing still sleeps the
  full 90 s and then boots, so an operator sees a long wait even with nothing
  holding the disks.

### 8.5 Manual take-over

`virtx takeover vm <uuid> --host <host>` and `POST /vms/{uuid}/takeover` move a
VM by hand, for an operator who knows what they are doing. It is not gated by
the switch. It refuses a VM whose owner is still active. A **dry-run mode**
(API only, `dry_run=1`) moves the registration and moves it back, starting
nothing; it records one dry-run entry in the VM's operation log. A manual
take-over always starts the VM, whatever state it was in: the register-only rule
is the decision layer's, because there nobody decided the VM should run.

### 8.6 When the victim comes back

The victim boots, registers again and starts publishing a report. Its old
domain definition is still in its libvirt, but its registration now names the
landing host, so the local-domain policy leaves that domain out of the host's
view and **keeps the definition**: an undefine can delete state that is still
needed (the firmware variables of a UEFI domain live outside the tree and are
not reproduced by defining it again). A warning is logged, and it is normal after a rescue.

On its very first snapshot after boot the host publishes its VMs under the host
uuid the snapshot itself already carries, because the machine identity is filled
in later; without that, the "was it running" record (8.2) would be missing for a
round.

---

## 9. The isolation response: what a cut-off host does by itself

Everything above is the cluster's view. A host that has concluded that it is
cut off must also act, because nobody else can stop its VMs: their disk leases
are renewed by its own sanlock daemon, and no other host can take them until it
lets them go.

### 9.1 Deciding "I am isolated"

Isolation needs two facts, and the code keeps them apart:

- `judges_silent`: the probe ran and no judge address answered,
- `isolated`: judges silent **and** the cluster is gone.

"The cluster is gone" is read from the peers' reports, never from serf and never
from the sanlock record. A host whose peer list cannot be read at all is not
isolated: there is nothing to lose. Losing the cluster is observed, never
assumed.

**Two exceptions.** First, an office holder: the peers it would ask are
precisely the ones it cannot reach, and its own report is not evidence about the
cluster. So an office holder whose judges are silent and that has no peer to answer for
it — or whose peer probes cannot run — concludes that it has been cut off,
resigns (9.3) and rescues nobody. An office holder whose judges still answer is
not isolated: two peers may die while the survivor keeps the office and rescues
them. Without this rule the last surviving host would keep the office and block
every rescue. That is the one place where "could not probe" counts.
Second, everyone else: a probe that could not run at all is not evidence of a
lost cluster.

### 9.2 What it does

Once `isolated` is set, and the action switch is on, the host waits
`ha_isolation_seconds` (default 14 s) from the first isolated round. The wait is
a grace period for a false isolation to clear before the host starts acting on
its own; it does not mean a VM stops after 14 s. A VM stops only after (a) the
isolation has lasted that long, (b) the cluster has claimed it, and (c) the claim
has been seen in two consecutive rounds.

Then it applies `ha_isolation_response` to the VMs it may act on:

| Response | Meaning |
|---|---|
| `leave-on` | do nothing (also the fallback for an unknown word: a typo must not power VMs off) |
| `shut-down` | ask the guest to stop; power it off if it has not after 300 s (note: the landing only waits 90 s for the disks, so a graceful stop that takes longer loses the race — the boot then fails on the image lock and the retry ladder covers it; use `power-off` when you want a fast rescue) |
| `power-off` | stop it outright (the default) |

But it stops a VM **only after the cluster has claimed it**. Each round it
reads, for every VM it runs, which host's directory holds the registration, and
it stops the VM when the same other host is named in two consecutive rounds (a
change of owner resets the count). That is the ordering from 8.4 seen from the
victim's side: the registration moving away is the proof that another host has
decided to take the VM over. A VM nobody claimed stays running, even on a host
that has lost the shared tree completely.

Two conditions may stop a VM, and the design keeps them separate:

- **the host learns that the VM has been taken over** — the registration names
  another host in two consecutive rounds. **This one is implemented.**
- **the host cannot reach the VM's datastore at all**, in which case the VM can
  no longer run anyway. **This one is not implemented yet**: the design states it
  (the comment above the response says `or when this host cannot read the storage
  at all`), the code does not do it.

What the code does today is the first condition only. When
`shared_tree_readable()` is false, `iso_one` keeps the VM and logs `noclaim`,
because a host that cannot read the tree cannot learn about a claim, and stopping
an unclaimed VM is the one thing the isolation response must never do. So the
datastore condition **stops nothing today**: the VMs keep running, and the case is
left to an operator (§13).

Two refinements:

- **Seen twice.** A move that is undone (a dry run, a rollback) looks the same
  for one round, and a drill must not make an isolated host stop a VM.
- **The offer.** The report carries the list of VMs the host runs while it is
  isolated (`isolation_running_vms`). That list is what the coordinator claims
  from. It accumulates while the isolation lasts, so a VM that was just claimed
  does not disappear from the list at the very moment it must be stopped. The
  report stops carrying it when the isolation ends; the internal list behind it
  is not reset at that point, so the next isolation appends to it. Harmless
  today, because an instruction for a VM whose registration is not under the
  victim is refused as `not-registered-there`, but it is a minor flaw worth
  knowing about.

The rescued VMs are not all started at once: after an isolation, the coordinator
starts them one at a time, `ha_isolation_start_interval_seconds` (default 5 s)
apart. Booting ten VMs at the same moment on one landing host would make the
cluster look like it had failed again; the pace is a cluster setting (section 10).

The rescue is therefore a conversation, not a command:

```
victim:      "I am isolated, and these are the VMs I run."   (report)
coordinator: "I claim this one."                            (instruction to a landing)
landing:     moves the registration, writes the claim
victim:      "I see the claim twice; I stop it."            (domain stops, leases go)
landing:     waits for the leases, boots the VM
```

### 9.3 An isolated host gives the office up

If the isolated host happens to be the coordinator, nobody else may take the
office while it holds it, and it can claim nothing for anybody. So an isolated
host resigns on its next round, with the reason in the log. The office moves to
a host that is still in touch, and the rescue can proceed.

---

## 10. Timing

The values, what they decide, and where they come from. "Tier" values follow the
sanlock tier the hosts actually run (10/60 shown).

| Value | Value at 10/60 | Source | What it decides |
|---|---|---|---|
| `ha_enabled` | `false` | policy | the action switch: shadow mode when off (3.5) |
| `ha_hastate_refresh_seconds` | 5 s | policy | how often a host writes its report and runs its loops |
| `ha_hastate_stale_seconds` | 30 s | policy | how long a report may stand still before it stops counting |
| `ha_isolation_address_0` .. `_9` | empty | policy | the judge addresses, one setting each; empty means the management gateway (3.3) |
| `ha_isolation_seconds` | 14 s | policy | how long an isolated host waits before its isolation response |
| `ha_isolation_response` | `power-off` | policy | what that response does: `leave-on`, `shut-down` or `power-off` |
| `ha_isolation_start_interval_seconds` | 5 s | policy | how far apart rescued VMs are started again |
| `ha_max_inflight` | 1 | policy | how many take-overs may run at once |
| renewal window | 20 s | tier | the second-confirmation spacing for a dead host |
| death threshold | 140 s | tier | sanlock's death threshold for a host_id lease |
| detection term | 30 s | code constant | one part of the office handover window: how long a detecting host waits before it is sure the holder stopped renewing |
| reset margin | 15 s | code constant | the reset margin after the first DEAD reading before a restart |
| attempt window | 180 s | derived | the deadline of one attempt, and the end of its key's window; also the office handover window |
| lease wait | 90 s | code constant | how long the start waits for the VM's disk leases |
| graceful-stop grace | 300 s | code constant | how long a graceful stop may take before a power-off |
| switch request deadline | 30 s | code constant | how long a request to move the office to a named host stays valid (7.5) |
| inventory freshness | 30 s | code constant | how old the inventory's record of a host may be before it is not a landing candidate (8.1) |

The attempt window is the detection term (30 s) + the death threshold + 10 s,
that is, it is derived from the same tier as the death threshold, so a cluster
on a different tier gets a window that grows with its death threshold. The
detection term and the 10 s margin are fixed, so only the threshold part moves,
and no code change is needed. It is the
time a detecting host needs to be sure that the old holder stopped renewing,
plus the death threshold, plus a 10 s margin: the point after which an
instruction from the old epoch is stale. The reset margin is neither a policy
value nor tier-derived: it is a fixed 15 s.

The intervals compose into this timeline for a host that dies:

```
death                                    (unobservable)
  |
  |  sanlock needs the death threshold (140 s) of failed renewals
  |  before any observer may read the host_id lease as DEAD
  |
  v
first DEAD reading                       ~140 s after the death
  |
  |  the second confirmation needs two readings at least one renewal window
  |  (20 s) apart; the reset margin (15 s) runs from the same first reading,
  |  so it is already spent when the confirmation completes
  |
  v
instruction issued                       about 2 min 40 s after the death
  |
  |  the landing moves the registration and writes the claim (instant)
  |  the landing waits for the disk leases. It is a short wait when the lease was
  |  released (an isolated victim after it stops the VM, or a VM that was shut
  |  off); for a VM that was running on the dead host the record stays held, so
  |  the wait runs its full 90 s and the boot then takes the lease. Only a boot
  |  that still fails on qemu's image lock goes to the retry ladder (7.2).
  |
  v
the VM boots          about four minutes after the death in the normal case (arithmetic: 140 s + 20 s + 90 s + a round)
                      (2 min 40 s + the 90 s lease wait, plus a round);
                      a boot that still fails can add two more attempts
```

For an isolated victim the dead-lease part is absent: the row comes from the
victim's own report (`fresh-1`) plus our failed ping, so the instruction can go
out within a round or two of the isolation. What adds time is the victim's 14 s
wait and the two rounds in which it must see the claim before it stops the VM.

### 10.1 A host dies: the whole story

One VM `1234` runs on `h11`; `h12` and `h13` are healthy; the switch is on.

| Time | Who | What it sees / does |
|---|---|---|
| t=0 | `h11` | powered off. Its VMs stop, its reports stop, its sanlock stops renewing. Nobody can observe this moment. |
| t≈5-30 s | `h12`, `h13` | `h11`'s report is still fresh for a few seconds, so the row is `Partitioned` (alive, cannot reach us, says it is not isolated) and the verdict `hold`; once its report has stood still for 30 s the row becomes `NoRow`, still `hold`. Its host_id lease is not DEAD yet. Nothing happens, by design. |
| t≈140 s | `h12`, `h13` | sanlock now reads `h11`'s host_id lease as DEAD. The row becomes `Failed`, but the judgement loop has not seen a second DEAD reading yet, so it downgrades the verdict to `hold` before the gate is asked. The console prints `hold h11: Failed`, the log says `verdict=hold`, and nothing has acted. |
| t≈160 s | `h12` (the office holder) | the second DEAD reading is in, the margin is spent. The gate passes. `h12` picks a landing (say `h13`), builds the key `(epoch, h11, 1234, 1)` and sends the instruction. |
| t≈160 s | `h13` (the landing) | re-checks the instruction, renames the registration of `1234` to itself, writes the claim naming `h11`, records the VM as powered off in the inventory, defines the domain, waits for the disk leases. |
| t≈160-250 s | `h13` | the lease record of `1234` still reads held (a dead host writes no release), so the wait runs its full 90 s, then the boot takes the lease and the VM starts. |
| t≈250 s | `h13` | clears the claim. `virtx log vm 1234` shows the move, the wait and the boot. |

If the VM had been shut off when `h11` died, the story stops at the define: the
registration moves, nothing boots, and the claim stays behind (8.2, 8.4).

**The same failure, measured.** On the three-host cluster (tier 10/60,
2026-10-17) `h12` was powered off while it ran one VM, and the
coordinator was `h11`. These are the coordinator's own log times, with the
power-off as zero:

| After | What the coordinator saw | Evidence |
|---|---|---|
| 3 s | `Partitioned`, `hold` | the report is still fresh, the ping already fails |
| 68 s | `NoRow`, `hold` | the report has gone stale |
| 98 s | `NoRow`, `hold` | the host_id lease has moved on to `FAIL` |
| 158 s | `Failed`, `hold` | the first `DEAD` reading |
| 183 s | `Failed`, `restart` | the second reading; the instruction is accepted in the same second |
| 275 s | the landing's 90 s lease wait ends | the disk leases still read held — a dead host releases nothing |
| 283 s | `landed`, `c4=running-at-landing` | the VM runs on the landing, the registration has moved |

Three things the derivation above does not show, and the run does:

- the first `DEAD` reading came **18 s after** the 140 s threshold — sanlock's
  own detection plus the interval at which the row is read;
- the 90 s lease wait is spent **in full**, and the boot then takes the lease
  anyway: the wait is hope, not a guarantee (3.4);
- the landing was the **coordinator itself** (`h11` held the office and took the
  VM), which the landing rule allows — a landing does not have to be a third
  party (8.1).

### 10.2 A host is isolated: the whole story

Same three hosts; `h11` loses its management network and its judges, but the
shared storage stays reachable for it. Its VM `1234` keeps running.

| Time | Who | What it sees / does |
|---|---|---|
| t=0 | `h11` | the judge probe fails (`judges_silent`), and it can reach no peer's management address, so the cluster is unreachable — but it can still read `/vms`. |
| t≈5 s | `h11` | from its first isolated round it publishes the offer — the VMs it still runs — because the claim has to be possible before the clock runs out. |
| t≈5 s | `h12`, `h13` | `h11`'s report is fresh and says `isolated=1`; the ping fails. The row is `Isolated`, the verdict `restart`. The gate needs no second confirmation and no lease evidence for this row. |
| t≈14 s | `h11` | the isolation has lasted long enough to *act*: it may now stop a VM, but only one the cluster has claimed and it has seen claimed twice. Nothing else is stopped. |
| t≈15 s | `h12` (the office holder) | claims `1234` from the offer: the landing (say `h13`) moves the registration and writes the claim. |
| t≈20-25 s | `h11` | sees the registration under the same other host twice in a row, so it stops the VM (by policy: power-off). The disk leases go. |
| t≈25 s | `h13` | the wait ends as soon as the leases are free, and the VM boots. |

If `h11` had also lost `/vms`, it could not see the claim, would never stop the
VM, and no rescue could complete from it — the VMs would simply keep running.
That is deliberate: without the tree there is no way to learn that a claim
exists (9.2).

This whole path was measured end to end on the three-host cluster, commands and
logs included: 12.3.

If the isolated host held the **office**, it resigns on its next round (9.3):
the lease is given back cleanly, another host may take the office on its next
round, and only the resigner keeps itself out of the contest for one handover
window (13). A dead holder is the different case: there the successor waits the
window out (7).


### 10.3 A host loses the storage, and nothing moves

`h11` keeps running, keeps its network and keeps answering, but its path to
`/vms` is gone.

| Time | Who | What it sees / does |
|---|---|---|
| t≈30 s | `h12`, `h13` | `h11`'s report has stopped changing, and it still answers the ping: that is `AgentDown` — the host is alive, its agent has stopped publishing. The verdict is `alert-only`. Its host_id lease is not DEAD yet. |
| t≈140 s | `h12`, `h13` | `h11`'s host_id lease reads DEAD — a host that cannot renew it — but the ping still answers, so the row is `HostAliveStorageLost`, not `Failed`. |
| t≈140 s and on | `h12`, `h13` | the row asks for a restart and the gate asks for `h11`'s disk leases to read free. They do not: a host that cannot reach the storage cannot write a release either. The row holds. |
| whenever | the operator | `virtx get ha` prints `hold h11: HostAliveStorageLost`, one line per VM. There is no automatic action; the operator decides whether to restore the path or to take the VMs by hand (8.5) once `h11` is out of the way. |

The row restarts only when the records really read free — which is what a host
that came back and released them looks like. In the meantime nothing is moved,
and that is the fail-closed answer: no evidence, no action.

### 10.4 The office holder dies

| Time | Who | What it sees / does |
|---|---|---|
| t=0 | `h11` | the host that held the office dies. Its host_id lease stops being renewed. |
| t≈140 s | `h12`, `h13` | the lease reads DEAD; `h11`'s report is stale and it does not answer, so the row is `Failed`. The verdict is `restart`, but nobody may act: no host holds the office. |
| t≈140 s | `h12`, `h13` | each starts its own handover clock: a host competes for the office only after it has seen the holder's host_id as unreachable for the handover window (180 s). |
| t≈320 s | `h12` (or `h13`) | one of them takes the office, bumps the epoch and can now act. The take-over of `h11`'s VMs starts here and then follows the story in 10.1. |

So a coordinator's death costs the handover window on top of the death threshold:
about five minutes before the first attempt can be sent, and the VM's own
take-over adds the lease wait to that. The two clocks are separate: the death
threshold (140 s) is sanlock's, the handover window (180 s) is the successor's
own wait, measured from its first DEAD reading.

The worst case is longer than the normal one: a take-over that keeps failing is
retried at most three times, and each attempt may last one attempt window. Reserve ten to fifteen
minutes before concluding that a take-over is stuck and reaching for a
hand-made one (8.5).

---

## 11. Safety properties

These are the invariants the system is built around. Each is explained in the
section named, and most of them exist because a test on the three-host cluster
showed what happens without them (§11.1 names the run each one rules out).

1. **Two live writers never share a disk.** The landing waits for the disk
   leases before booting, and a boot that still races a live writer fails on
   qemu's image write lock instead of writing (3.4, 8.4). Over NFS that lock is
   only as strong as the NFS server; the lease wait is the first barrier, the
   lock the last.
2. **Only one host acts.** The coordinator lease is a real lock on the shared
   storage; a dead coordinator's lease stops being renewable at the death
   threshold, and a successor waits the handover window from the moment it first
   sees the holder as unreachable before it competes (7).
3. **An instruction is executed at most once inside its window.** The
   idempotency key, checked by the landing for one attempt window. Outside its
   window a late instruction is refused by its own deadline; a *known* older
   epoch is refused too, though an unreadable lock-value block leaves the epoch
   unknown and the epoch check with nothing to compare (7.1, 13).
4. **An isolated host never stops a VM nobody claimed.** The claim is the
   permission to stop; the offer is the permission to claim (9.2).
5. **Fail closed.** A policy that cannot be read leaves the host with its applied
   copy, and with neither an applied copy nor a file the switch is off. An
   unreadable host_id lease produces `hold`, not `restart`. A host whose
   ownership cannot be shown is acted on by nobody (3.5, 5).
6. **Nothing acts on a guess.** A probe that could not run is not evidence; a
   second confirmation is required for a DEAD host; a stale report is not a dead
   host (5, 6, 9.1).

### 11.1 What each property excludes

A property is worth stating only together with the run it rules out. This is the
same list as above, read the other way round.

| Property | The interleaving that would break it | The mechanism that excludes it |
|---|---|---|
| two live writers never share a disk | the old VM is still running when the landing boots the same VM | the landing waits for the disk leases, and the boot must take the lease; a writer that is still alive keeps renewing it (3.4, 8.4) |
| only one host acts | two hosts both decide to move the same VM | the coordinator lease has one holder, the epoch fences the old one out, and the registration rename has one winner (7, 8.4) |
| an instruction runs at most once per window | the coordinator retries and the same instruction is executed twice | the landing remembers the key for one attempt window; a late copy is refused by its deadline, an old one by its epoch (7.1) |
| an isolated host stops only claimed VMs | a cut-off host stops VMs nobody has taken over | it reads the registration owner every round and needs the same other host twice; an unreadable tree means it stops nothing (9.2) |
| fail closed | a record cannot be read and the cluster acts anyway | a row that rests on an unreadable record is `hold`; no readable policy means the switch is off (3.5, 5) |
| nothing acts on a guess | a probe fails and is taken as a fact | a probe that did not run is not evidence, a single DEAD reading is not enough, and a stale report is not a death (5, 6, 9.1) |


---

## 12. Watching it work

- **`virtx get ha`** prints the switch, the coordinator, the policy generation,
  which host has applied which generation (`APPLIED`; read from each host's
  report, not from a local file), how many hosts have acknowledged it (`ACKED`),
  which have not (`NOT YET`, listed by name), and the judgements that ask for
  something. A healthy cluster prints nothing about its peers: quiet is the
  normal state.
- **`virtx get ha -D`** adds the evidence columns for each host: what its report
  says, what the host_id lease says, and what the management probe answered (the
  probed address is printed when the address is known and the host did not
  answer, which includes a probe that could not run). The one-line explanation
  of a `StorageUnreadable` or `PartitionStorageUnreadable` row is printed
  without `-D`; `-D` is the evidence behind it.
- **`virtx enable ha` / `virtx disable ha`** change the switch and report which
  hosts have acknowledged the change (3.6).
- **`virtx set ha --coordinator <host>`** asks the coordinator office to move to
  that host (7.5): the current holder resigns, that host takes the lease, and
  the other hosts stay out of the contest until it does or the request runs out.
  The command waits for the move and says `NOT CONFIRMED` when it has not happened
  yet.
- **The daemon log** (wherever virtxd's output goes in your deployment) prints
  one line per judgement change. The gate's reason is one of:
  `not-restart` (the verdict is not a restart), `armed-off` (the switch is off),
  `no-evidence` (the row's lease evidence is missing), `t-takeover` (the `Failed` margin has not been served). Each attempt
  is logged with its result: `accepted`, `refused`, `landed`, `timed-out`, or
  `pending` with a reason.
- **`virtx log vm <uuid>`** is the per-VM operation log; a take-over writes its
  moves and its failures there, and the record travels with the VM's directory.

A typical exercise on a three-host cluster:

```
virtx enable ha            # arm the cluster; watch the acknowledgements
virtx get ha               # the switch, the coordinator, the generation
# ... cut one host's network, or power it off ...
virtx get ha               # the row of that host, and the judgement that follows
virtx get ha -D            # the evidence behind that row
# daemon log: the coordinator's take-over lines
virtx log vm <uuid>        # the per-VM record of the move
```

On a healthy, armed three-host cluster, the command prints six lines and
nothing else (this is a verbatim capture from the test cluster; `|` is the
column separator the command uses, and the extra `|` in `APPLIED ||` is the
first character of its value, which is a list):

```
HA RESPONSE |on
COORDINATOR |h11 (4da6aaa0-1799-4859-9689-6a652834f10a)
GENERATION  |3
APPLIED     ||h11: 3, h12: 3, h13: 3
ACKED       |3
NOT YET     |0
```

With one host's daemon stopped — the host is still up, its report is not being
written — the same command shows a judgement and the acknowledgement it has
lost:

```
HA RESPONSE   |on
COORDINATOR   |h11 (4da6aaa0-1799-4859-9689-6a652834f10a)
GENERATION    |3
APPLIED       ||h11: 3, h12: 3, h13: 3
ACKED         |2
PENDING       |yes: waiting for generation 3
NOT YET       |1
  waiting for |1037a0bf-78cd-4e8a-9b79-561bb97f01b3 (h13)
alert         |h13: AgentDown
```

The first word of a judgement line is the verdict's word: `action` for
`restart`, `alert` for `alert-only`, `hold` for everything else. Only judgements
that are not `no-op` are printed at all, which is why a healthy cluster looks
quiet. The `PENDING` line appears because `ACKED` counts fresh reports: the host
whose daemon stopped still has generation 3 applied, but it is not publishing
it, so the cluster reads the change as outstanding (§3.6).

With `-D` the same lines gain the evidence they rest on, and `COORDINATOR` gains
the epoch; `-D` also turns on the debug log on stderr, which is why a `-D` run
prints log lines above the table:

```
COORDINATOR   |h11 (4da6aaa0-1799-4859-9689-6a652834f10a), office epoch 20041
...
alert         |h13: AgentDown, report stale, host lease LIVE, mgmt reachable, age 30s
```

The epoch is 20041 on this cluster because it has taken the office many times
over its lifetime; the counter is stored in the lease and never reset. The age
is measured on **our** clock, from when we first saw the report's current
content — never from the peer's own timestamp — and it is printed in whole
seconds, so `30s` is a reading just past the 30 s staleness threshold.

### 12.1 Why nothing happened

When you expected a take-over and none came, ask these in order; the first one
that fires is the reason:

1. **Is the switch on?** `HA RESPONSE off` means shadow mode: everything is
   judged and logged, nothing is moved.
2. **Is there a judgement line at all?** None means every row is `no-op`, i.e.
   there is nothing to look at. In the first two minutes after a death you will
   see a `hold` line rather than silence: `Partitioned` while the report is still
   fresh, `NoRow` once it has stood still for 30 s.
3. **Is the row a `hold`?** Read its name: `NoRow` (nothing known yet),
   `Partitioned` (alive, cannot reach us, says it is not isolated),
   `StorageUnreadable` (a record could not be read).
4. **Is the row `Failed` but nothing moved?** First look at the verdict in the
   log. If it is `hold`, the second confirmation has not landed yet — wait one
   more renewal window (20 s). If it is `restart`, the gate refused, and its
   reason names the check: `armed-off` (the switch, §3.5), `no-evidence` (the
   row's lease evidence), or `t-takeover` (the 15 s reset margin, which only
   binds at a tier whose confirmation is shorter than 15 s — at 10/60 the 20 s
   confirmation is the longer wait).
5. **Who holds the office?** `COORDINATOR none`, or a holder that is itself
   gone, means nobody can act. This is the easiest cause to miss. A host that is
   not the holder logs `action=not-coordinator` and drops the round, which looks
   like a refusal and is not one.
6. **Did an attempt run?** The log says `accepted`, `refused`, `landed` or
   `timed-out`; a `timed-out` is followed by a retry.
7. **Did the landing refuse it?** Then the log names the refusal (8.3).
8. **Is the victim cut off from the tree?** Then it cannot see the claim, never
   stops the VM, and the rescue cannot complete from it (9.2).

Three more labels appear when they apply: `COORDINATOR none` (nobody holds the
office), `BASELINE yes` (no policy has been agreed yet, generation 0) and
`PENDING yes` (a switch change is on its way). A host that is itself cut off
prints `THIS HOST |isolated: ...` once at the top.

### 12.2 The same failure, line by line

The drill in 10.1 left five kinds of line in the coordinator's log and one line
on the console. Here they are, in the order they appeared, with every field
explained. The host names are the drill's (`h12` is the host that died, `h11`
is the coordinator and the landing).

**1. The judgement, while nothing may happen yet.**

```
15:17:18 verdict: host=25983c36-… name=h12 row=Partitioned verdict=hold armed=true
         admission=n/a report_fresh=true lease=LIVE report=fresh-0
         mgmt_known=true mgmt_probed=true mgmt_reachable=false
```

| Field | Meaning here |
|---|---|
| `host`, `name` | the peer being judged (`name=me` for the host itself) |
| `row`, `verdict` | the decision-table row and the verdict it maps to (5) |
| `armed` | the policy switch at the moment of judgement; `false` means shadow mode |
| `admission` | the capacity/placement admission check; `n/a` today because it is not wired (8.1) |
| `report_fresh` | the self-report's fingerprint is within the 30 s staleness window |
| `report` | the report state: `fresh-0` (fresh, nothing changed), `stale`, `absent` |
| `lease` | the host_id lease state: `LIVE`, `FAIL`, `DEAD`, `UNKNOWN` |
| `mgmt_known`, `mgmt_probed`, `mgmt_reachable` | whether we know the management address, whether we asked, and the answer |

The same line comes back at 68 s, 98 s, 158 s and 183 s with one field changed
each time: `report=stale` (68 s), `lease=FAIL` (98 s), `lease=DEAD` with
`row=Failed` (158 s), and finally `verdict=restart` (183 s).

**2. The landing re-checks the instruction before it touches anything.**

```
15:20:18 vm_takeover: landing vm=f618… row=Failed epoch=4 known=4 mask=1792250598425
         now=1792250418488 lease=no lease_free=false forward=true already_running=false
         third_party=false capacity=true refusal=""
```

| Field | Meaning here |
|---|---|
| `epoch`, `known` | the epoch the instruction carries, and the epoch read from the coordinator lease's lock-value block (`0` means unknown, 13) |
| `mask`, `now` | the deadline the instruction must be executed by, and the landing's clock when it checked |
| `lease` | the victim VM's disk leases as this host reads them: `yes` free, `no` held, `unknown` unreadable |
| `lease_free`, `forward` | the two-valued form of that reading, and whether it is evidence at all (`forward=false` when unreadable) |
| `already_running` | the domain already runs here (a duplicate instruction) |
| `third_party` | a host other than the victim is still renewing one of its leases |
| `capacity` | room on this host; `true` by constant today (8.1) |
| `refusal` | empty when admitted; otherwise the code 8.3 names |

**3. What the victim's record said when the move began.**

```
15:20:18 vm_takeover: landing vm=f618… victim=25983c36-… row=Failed record=running
         record_ok=true lease=no was=was-running starts=true dry_run=false
```

`record` is the victim's recorded run state; `record_ok` says the record could
be read; `was` is the "was it running" reading that decides whether this
take-over starts the VM; `starts=true` means it will; `dry_run=true` would mean
a drill that undoes itself.

**4. The instruction, and later its result.**

```
15:20:18 action vm=f618… victim=25983c36-… epoch=4 key="epoch=4 victim=… vm=f618… attempt=1"
         attempt=1 landing=4da6aaa0-… row=Failed was=was-running expectation=running-at-landing
         result=accepted mask_deadline_ms=1792250598425 elapsed_ms=97
15:21:58 action vm=f618… result=landed c4=running-at-landing moved=true elapsed_ms=100121
```

`attempt` is the attempt number inside the epoch; `key` is the idempotency key
(7.1); `expectation` is what "done" means for this VM (`running-at-landing` for
a VM that was running, `registered-off` for one that was off); `result` goes
`accepted` → `landed` or `timed-out`; `c4` is the landing-confirmation channel
(`running-at-landing`, `off-at-landing`, `unreadable`, …); `moved` says the
registration now sits at the landing; `elapsed_ms` is the attempt's duration.

**5. The round summary.**

```
15:22:03 driven host=25983c36-… name=h12 row=Failed verdict=restart action=driven
         row=Failed epoch=4 vms=0 planned=0 pending=0 inflight=0
```

`action` is what this round did with the verdict: `driven` (acted),
`pending` (an attempt is in flight, so the next one waits), `not-coordinator`
(another host holds the office), or a gate reason. `vms`, `planned`, `pending`
and `inflight` count the VMs in that state — `vms=0` is how "there is nothing
left to do" reads.

**6. The console, while all of this happened.**

```
hold   |h12: Partitioned - needs an operator        (3 s … 68 s)
hold   |h12: NoRow                                 (68 s … 158 s)
hold   |h12: Failed                                (158 s … 183 s)
action |h12: Failed, virtual disk leases unknown - that host no longer holds
                the registrations (claimed)        (after 183 s)
```

The first word is the verdict; the row is the reason; the tail after the comma
is the gate's or the landing's reason when there is one. `hold …: Failed`
before the `action` line is not a stuck attempt — it is the confirmation window
(6).

### 12.3 A network isolation, start to finish

This is a drill that was actually run, with the commands, the timeline and the
logs. It is the isolation story of 10.2, measured.

**The cluster.** Three hosts, one management network and one storage network:
`h11` (management `192.168.31.111`, storage `192.168.126.11`), `h12`
(`.112` / `.126.12`), `h13` (`.113` / `.126.13`). The switch was on, the office
holder was `h13`, and all three had acknowledged the policy. `h12` ran two VMs
(`gold-leap16-i440fx`, `gold-sles15sp7-efi`) and had a third one registered and
powered off (`gold-sles16-efi`).

**How the isolation was made.** On `h12`, two addresses were cut: the office
holder (`.113`) and the judge (the management gateway `.1`). The storage network
was left alone, which is the interesting part: `h12` can still read `/vms`, so it
can still see the claims. These are the exact commands, run as root on `h12`:

```
nft add table inet ha_isolation_demo
nft add chain inet ha_isolation_demo out '{ type filter hook output priority 0 ; policy accept ; }'
nft add chain inet ha_isolation_demo in '{ type filter hook input  priority 0 ; policy accept ; }'
nft add rule inet ha_isolation_demo out ip daddr 192.168.31.113 drop
nft add rule inet ha_isolation_demo in ip saddr 192.168.31.113 drop
nft add rule inet ha_isolation_demo out ip daddr 192.168.31.1 drop
nft add rule inet ha_isolation_demo in  ip saddr 192.168.31.1 drop
```

**What the victim decided, and when.** Seven seconds after the rules went in,
`h12` published its first isolated round:

```
21:16:55 isolation: this host is isolated; the response (power-off) starts 14 s after
         the first such round …
21:16:55 hastate: change: probed=1 isolated=1 h13(89) LIVE fresh-0 | me(1142) LIVE
         fresh-0 | h11(1856) LIVE fresh-0 | leases: …
```

Note what did *not* make `h12` isolated: `h11` was still reachable the whole
time. What matters is the host the shared tree names as the office holder — a
host that cannot reach the office holder, and whose judges are silent, is on its
own (9.1).

The offer went out in the next report — the VMs it runs, because that is what the
cluster may claim:

```
21:17:05 hastate: change: … | me(1142) LIVE fresh-1 isolated | h11(1856) LIVE fresh-0 |
         … iso-runs=3b2f2da0-…,df9a3882-…
```

**What the cluster did.** The office holder `h13` read `h12` as `Isolated` and
claimed the first VM:

```
21:17:06 verdict: host=25983c36-… name=h12 row=Isolated verdict=restart armed=true action=restart
21:17:07 action vm=3b2f2da0-… victim=25983c36-… landing=4da6aaa0-… row=Isolated
         was=was-running expectation=running-at-landing result=accepted … elapsed_ms=233
```

On the console that is one line:

```
action |h12: Isolated, report fresh-1, host lease LIVE, mgmt network unreachable:
              no reply from 192.168.31.112, age 0s, virtual disk leases free —
              nothing holds the disks of its VMs
```

**What the victim did with it.** At 21:17:10 its response clock (14 s) ran out and
it looked at its VMs; nobody had claimed anything yet, so it kept them:

```
21:17:10 isolation: 3b2f2da0-…: the VM is still registered here: nobody has claimed it, keeping it
21:17:10 isolation: df9a3882-…: the VM is still registered here: nobody has claimed it, keeping it
21:17:10 isolation: 943ffbb2-…: the VM is still registered here: nobody has claimed it, keeping it
```

The claim for the first VM arrived, and the two-round rule held it for one more
round — a move that is undone looks the same for one round (9.2):

```
21:17:25 isolation: 3b2f2da0-…: the registration moved to 4da6aaa0-… once: waiting one more round before stopping it
21:17:30 isolation: 3b2f2da0-… is registered under 4da6aaa0-… now: it has been claimed, stopping it here
```

**What the landing did.** It had been waiting since the claim, and the disk
leases went free with the stop:

```
21:17:06 vm_takeover: landing vm=3b2f2da0-… row=Isolated … lease=no lease_free=false forward=true …
21:17:30 vm_takeover: 3b2f2da0-…: the leases of the VM are free=true after waiting
21:17:32 vm_takeover: takeover of domain 3b2f2da0-… from h12 successful
```

Then the same sequence for the second VM, one at a time and a few seconds apart:

```
21:17:42 action vm=df9a3882-… result=accepted …            (the coordinator's claim)
21:17:55 isolation: df9a3882-…: the registration moved to 4da6aaa0-… once: waiting one more round
21:18:00 isolation: df9a3882-… is registered under 4da6aaa0-… now: it has been claimed, stopping it here
21:18:01 vm_takeover: df9a3882-…: the leases of the VM are free=true after waiting
21:18:02 vm_takeover: takeover of domain df9a3882-… from h12 successful
```

Both results came back as landed, with the fourth channel reading "running at the
landing":

```
21:17:37 action vm=3b2f2da0-… result=landed c4=running-at-landing moved=true elapsed_ms=30241
21:18:07 action vm=df9a3882-… result=landed c4=running-at-landing moved=true elapsed_ms=25288
```

**The timeline**, with the rule insertion as zero:

| After | What happened | Evidence |
|---|---|---|
| 7 s | `h12` publishes its first isolated round | `isolated=1` in the report |
| 17 s | the offer appears in the report | `iso-runs=3b2f2da0…,df9a3882…` |
| 18 s | `h13` reads `Isolated`, claims VM 1 | `result=accepted`, 233 ms |
| 21 s | the victim's 14 s clock runs out; it keeps both VMs | "nobody has claimed it, keeping it" |
| 37 s | the victim sees VM 1's claim once | "waiting one more round" |
| 42 s | the victim confirms and stops VM 1 | "it has been claimed, stopping it here" |
| 44 s | the lease wait ends, VM 1 boots at the landing | `free=true`, then "successful" |
| 49 s | the coordinator records VM 1 landed | `elapsed_ms=30241` |
| 53 s | `h13` claims VM 2 | `result=accepted`, 280 ms |
| 72 s | the victim stops VM 2 | 21:18:00 |
| 74 s | VM 2 boots at the landing | "successful" |
| 79 s | VM 2 landed | `elapsed_ms=25288` |

**Two things this drill shows about the design.**

- The **lease wait was short, not 90 s**, because the victim stopped the VM and
  released the leases before the landing finished waiting: the landing's wait is
  a maximum, and a rescue that goes well costs seconds, not the full window. It
  is the *crash* case (10.1) where the dead host releases nothing and the wait
  runs out.
- The **claims are serialised**: one attempt at a time (`ha_max_inflight`), a few
  seconds apart (`ha_isolation_start_interval_seconds`), so a landing that takes
  over ten VMs does not boot them all in the same second.

**And one thing it deliberately does not do.** The third VM, `gold-sles16-efi`,
was registered on `h12` and powered off. It is not in the offer (the offer lists
the VMs the host is *running*), so no claim is ever made for it, and it stays
registered on `h12` — `virtx list vm` still shows it under `h12`, `poweroff`,
after the drill:

```
943ffbb2-4f79-439b-bfc1-db87eb330ea1 | gold-sles16-efi | 25983c36-… | h12 | poweroff
```

That is the rule from 8.2: a VM that was off when its host was cut off was off
*on purpose*, and nobody decided it should run.

**What the isolated host itself saw.** Its own `virtx get ha` is honest about
where the view comes from — and it is the only host that prints the `THIS HOST`
line:

```
HA RESPONSE |on
COORDINATOR |h13 (1037a0bf-…)
THIS HOST   |isolated: it cannot reach the management network or its judges; its view of the others is the shared ledger only
GENERATION  |1
APPLIED     ||h11: 1, h12: 1, h13: 1
ACKED       |3
NOT YET     |0
hold        |h13: Partitioned - needs an operator
```

The last line is worth reading twice: from the isolated host, the *coordinator*
looks like a partitioned peer (it cannot be reached, and its report says it is
not isolated) — the same row the coordinator would have used for `h12` had `h12`
not said it was isolated.

---

## 13. What is not here (proof-of-concept scope)

- **No out-of-band fencing.** The cluster never cuts power to a host; it relies
  on the host's own watchdog, the sanlock leases and the isolated host acting on
  itself.
- **The second lease check is not implemented.** When the lease table cannot be
  read, the two rows that need it (`HostAliveStorageLost`,
  `PartitionStorageUnreadable`) hold instead of re-reading the table to break the
  tie; `Failed` and `Isolated` never need it. The cost is delay, not safety.
- **Capacity is uncalibrated.** The capacity check passes everybody by a named
  constant, and the free-capacity ordering key uses memory only (the CPU
  dimension is not wired).
- **Expected placement is not wired.** The first ordering key is always false.
- **The "disabled / in maintenance" landing filter is not implemented.** No host
  is excluded for being in maintenance; the filter passes everybody.
- **A named successor is a request, not a rule.** `virtx set ha
  --coordinator <host>` (7.5) makes the current holder resign, and the named
  host takes the office on its next round; the others stay out of the contest
  until it does, or until the request's 30 s deadline passes. Nothing pins the
  office to a host: a target that does not come lets the others compete after
  that wait, and the lease remains the only authority.
- **A clean resign has a messy fallback.** The lease is kept alive by a small
  helper process on the host's behalf (sanlock's own client). If that process
  cannot be identified or verified, or if ending it fails, the lease
  registration is left behind as an orphan, and only the startup sweep (7.4)
  gives it back.
- **The retry ladder is short:** three attempt numbers, one VM at a time by
  default.
- **The per-VM "HA-unprotected" marker is not implemented.** Every VM on an
  affected host is eligible.
- **libvirt's pause-on-lock-failure policy is not emitted.** That setting is
  commented out in the domain XML; the code relies on a failed boot instead.
- **The isolation offer is not reset internally** when an isolation ends; only
  the report stops carrying it (9.2).
- **Not implemented yet: the "datastore unreachable" stop.** The design (and the
  comment above the response) says a VM may also be stopped when the isolated
  host cannot read the storage at all. The code does not do it: the gate keeps the
  VM when the tree is unreadable (`noclaim`), because a host that cannot read the
  tree cannot learn about a claim, and stopping an unclaimed VM is forbidden
  (9.2). That case is left to an operator.
- **A landing defines the domain XML as it finds it.** Nothing checks that the
  landing's libvirt/qemu can define or boot that XML (a different machine type,
  a missing bridge, a disk path that is not there). When the define fails, the
  registration has already moved and stays there; only a hand-made take-over
  moves the VM again.
- **An epoch that cannot be decoded is treated as unknown, and the next holder
  starts counting again at 1.** The epoch lives in the lease's lock-value block;
  a block that cannot be decoded leaves the landing with "no epoch known" (an
  instruction is then not refused for carrying an old epoch), and the next
  holder that takes the office begins a fresh count. Until the count passes the
  old value, an instruction from a deposed coordinator can look newer than the
  live one.
- **A host directory is never removed for you.** The peer set is the set of
  directories under `/vms/reg`, so a decommissioned host that shut down cleanly
  (and therefore never reads `Failed`) stays a peer that holds forever, until an
  operator removes its directory.
- **The landing's key memory is in RAM too.** A landing that restarts during an
  attempt forgets the keys it had executed, exactly like the coordinator
  (7.1); a duplicate instruction that arrives after the restart is refused by
  its deadline, not by the memory.

---

## 14. Code map

| Package | Responsibility |
|---|---|
| `pkg/policy` | read, validate, apply and write the cluster policy, with the applied copy and the request file |
| `pkg/hastate` | self reports, judge probing, the isolation conclusion and the isolation response |
| `pkg/verdict` | the decision table, the verdicts, the time windows, the gate |
| `pkg/coord` | the coordinator office, the action half (landing rule, instruction, idempotency, coordinator loop), and the request that moves the office |
| `pkg/lockman` | sanlock access: leases, the coordinator resource, the effective tier, the stale-lease sweep |
| `pkg/reg` | the shared registration tree: VMs, reports, claims |
| `pkg/inventory` | the cluster view of hosts and VMs, used by the landing rule and the capacity key |
| `pkg/hypervisor` | libvirt domains, the per-host snapshot, the local-domain policy |
| `pkg/virtx` | the REST API, including the take-over endpoint and `get ha` |
| `pkg/ha` | the switch: state, change request, waiting for acknowledgements |
| `cmd/virtx` | the CLI: `get ha`, `enable ha`, `disable ha`, `set ha --coordinator`, `takeover vm`, `log vm` |
| `cmd/virtxd` | startup wiring: tier, configuration, action half, loops |

### 14.1 Where each mechanism lives

The section names a mechanism; this table names the code that implements it.

| Mechanism | Where | The function |
|---|---|---|
| the self report is written | `pkg/hastate/hastate.go` | `observe`, `Loop` |
| the published view other packages read | `pkg/hastate/publish.go` | `Published_view` |
| a peer's report is classified | `pkg/hastate/hastate.go` | `Classify` (the fingerprint is built in it), `read_state_path` |
| the judge probe | `pkg/hastate/isolation_address.go` | `judge_addresses`, `default_gateway` |
| "am I isolated?" | `pkg/hastate/cluster.go` | `cluster_unreachable`, `Self_isolated` |
| the isolation response and the offer | `pkg/hastate/isolation.go` | `isolation_round`, `iso_one`, `offer_prune` |
| the policy is read and applied | `pkg/policy/cluster_policy.go`, `cluster_policy_applied.go`, `cluster_policy_reader.go` | `Parse`, `Load_applied`, `May_apply` |
| the policy is written | `pkg/policy/cluster_policy_write.go` | `Render` |
| the switch request and its acknowledgement | `pkg/ha/ha.go` | `Request`, `Apply_pending`, `clear_reports` |
| the sanlock tier and the windows | `pkg/lockman/sanlock_tier.go`, `pkg/verdict/sanlock_tier_window.go`, `pkg/coord/sanlock_tier_window.go` | `Read_tier`, `Set_tier`, `Check_sanlock_tier_windows` |
| the lease table is read | `pkg/lockman/hosts.go` | `Inq_hosts`, `Inq_leases` |
| free / held / unknown for a VM | `pkg/verdict/verdict.go` | `Lease_state`, `Leases_free` |
| the lock-value block | `pkg/lockman/lockman.go` | `Read_lvb`, `Write_lvb` |
| the observations | `pkg/verdict/verdict.go` | `Columns`, `columns_of` |
| the table | `pkg/verdict/verdict.go` | `Select_row` |
| a row becomes a verdict | `pkg/verdict/verdict.go` | `Verdict` |
| the second confirmation | `pkg/verdict/verdict.go` | `confirmed_dead` |
| the gate | `pkg/verdict/action.go` | `Gate`, `Evidence_ok`, `t_takeover_elapsed` |
| the round loop | `pkg/verdict/verdict.go` | `Loop`, `drive_rows` |
| the office: take, release, publish | `pkg/coord/actuate.go` | `take`, `release`, `publish`, `next_epoch` |
| the office state | `pkg/coord/coord.go` | `COORD_RESOURCE`, `Observed` (the `OBS_*` constants), the office loop |
| the resign | `pkg/coord/resign.go` | `Resign`, `Wait_resign` |
| the office switch | `pkg/coord/switch.go` | `Save_switch`, `Load_switch`, `Switch_target`, `switch_elsewhere` |
| the idempotency key | `pkg/coord/action.go`, `pkg/coord/idem.go` | `Idempotency_key`, `next_free_attempt`, `New_idem_window` |
| the orphan sweep | `pkg/lockman/stale.go` | `Release_stale_registrations` |
| the landing choice | `pkg/coord/action.go`, `pkg/coord/landing_pick.go` | `landing_candidates`, `Plan_round`, `count_inflight`, `Pick_landing` (with `eligible`) |
| the registration-conflict check | `pkg/reg/reg.go` | the "registered under two hosts" error the tree is checked for |
| the atomic write of a file in the tree | `pkg/reg/reg.go` | `reg_save_file` (temporary file, then rename) |
| what the landing observes | `pkg/coord/observe.go` | `Observe_landing`, `evidence_from_leases`, `lease_owner_is_live` |
| the graceful end of a holder | `pkg/coord/handover.go` | `end_holder_gracefully` |
| before the move: where, and may it start | `pkg/coord/landing.go`, `pkg/virtx/vm_takeover.go` | `Check_landing`, `Takeover_state`, `placement_of` |
| the take-over itself | `pkg/coord/issue.go`, `pkg/virtx/vm_takeover.go` | `Issue_takeover`, `vm_takeover_run`, `wait_for_free_leases` |
| the commands | `cmd/virtx/ha.go`, `cmd/virtx/cmd.go` | `ha_show`, `ha_judgement_line`, the `log vm` command |
| startup wiring | `cmd/virtxd/main.go` | `Set_tier`, `Check_sanlock_tier_windows`, `Wait_resign` |

A value in section 10 whose source is "tier" cannot be changed in the source
alone: it is recomputed at startup from the sanlock tier the hosts run
(`Set_tier`), and `Check_sanlock_tier_windows` compares the derivation with
itself. Changing the sanlock tier on the hosts is what changes it.

---

## 15. Index of terms

| Term | Meaning | Section |
|---|---|---|
| **action half** (the drive in the code) | the coordinator's half of the round, called by the judging host | 4, 6 |
| **applied copy** | the policy a host last applied, kept locally for when the shared file cannot be read | 3.5 |
| **attempt** | one numbered try at a take-over; a retry is a new attempt number, which is a new key | 7.2 |
| **attempt window** | the deadline of one attempt, and the end of its idempotency key's window (also called the attempt deadline) | 7 |
| **claim** | the record a landing host writes when it takes a VM over from another host | 8.4 |
| **coordinator loop** | the coordinator's own loop: hold or take the office, settle attempts, publish the attempt deadline | 4, 7 |
| **coordinator office** | the sanlock lease that gives exactly one host the right to act | 7 |
| **death threshold** | sanlock's 140 s: 8 missed renewals plus the watchdog timeout | 3.4 |
| **detection term** | the 30 s inside the handover window, to be sure the old holder stopped renewing | 7 |
| **epoch** | a counter bumped whenever the office changes hands | 7 |
| **fence** | to make sure a failed host cannot touch shared storage any more; this POC does not fence out of band | 13 |
| **fresh-0 / fresh-1** | the two fresh report states: the host says it is not, or is, isolated | 3.2, 5 |
| **gate** | the check that turns a verdict into an action, or refuses to | 6 |
| **generation** | the cluster policy's version counter, which only moves forward | 3.5 |
| **handover window** | the 180 s a host waits after seeing the office holder unreachable before it competes for the office; the same window keeps a resigning host out of the contest | 7, 10 |
| **hold** | the verdict "do nothing and explain"; unrelated to holding a lease | 5 |
| **host_id lease** | sanlock's lease that proves a host is alive | 3.4 |
| **idempotency key** | the tuple (epoch, victim, VM, attempt) that lets the landing refuse a repeated instruction | 7.1 |
| **inventory** | the cluster's view of hosts and VMs, fed by the hosts' published `vminfo` | 8.1, 8.2 |
| **isolation** | a host that is alive but cut off from both the cluster and its judge addresses | 1.1, 9 |
| **judge address** | a non-cluster address a host pings to decide whether its own network is broken | 3.3 |
| **landing host** | the host that will run the VM after a take-over | 8.1 |
| **lease** | a renewable record on shared storage that only one holder can own | 3.4 |
| **lease registration** | sanlock's own bookkeeping for a lease it handed out; unrelated to a VM registration | 7.4 |
| **lease table** | the list of all lease records, read once per round | 3.4 |
| **lock-value block** | the writable field inside a lease record; the coordinator publishes the epoch and attempt deadline there | 3.4, 7 |
| **lockspace** | the set of leases a cluster shares | 3.4 |
| **NoRow** | the row for "the observations establish nothing actionable" | 5 |
| **offer** | the list of VMs an isolated host publishes as still running | 9.2 |
| **office switch** | a request that the coordinator office move to a named host; the holder resigns, that host takes the lease, and the others wait for it | 7.5 |
| **partition** | a host that is alive but cannot reach this host while the rest of the cluster is fine | 5 |
| **registration tree** | `/vms/reg`: the shared directory tree that says which host owns which VM | 3.1 |
| **renewal window** | the spacing of the second DEAD confirmation (2 × io_timeout, 20 s) | 3.4, 6 |
| **report** | the small JSON object a host writes about itself every few seconds | 3.2 |
| **rescue** | a take-over from an isolated, still-running host | 8.2, 9 |
| **reset margin** | the 15 s a `Failed` host must have been read DEAD before a restart | 6 |
| **row** | the classification of one peer in one round | 5 |
| **sanlock** | the daemon that hands out leases on shared storage | 3.4 |
| **second confirmation** | the second DEAD reading, one renewal window after the first | 6 |
| **serf** | VirtX's gossip layer; used to find addresses, never to decide | 2 |
| **shadow mode** | the cluster with the switch off: it judges and logs, and never acts | 3.5 |
| **switch** | `ha_enabled` in the cluster policy: whether the hosts may act | 3.5 |
| **tier** | sanlock's pair of settings (`io_timeout`, `watchdog_fire_timeout`) | 3.4 |
| **verdict** | what a row allows: no-op, hold, alert-only or restart | 5 |
| **victim** | the host the VM is being taken from, dead or isolated | 7.1 |
| **watchdog** | the hardware timer that resets a host whose sanlock daemon stopped petting it | 3.4 |

