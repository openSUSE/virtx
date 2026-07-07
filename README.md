# XXX EXPERIMENT/PROTOTYPE: DO NOT USE XXX

This is still very much experimental and likely insecure, buggy, needing code review.
Use at your own risk! You have been warned!

# LICENSE

This code is under the GPLv2 license. See LICENSE file for details.

The following dependencies are included:

serf client library, Copyright (c) 2013 HashiCorp, Inc.
https://pkg.go.dev/github.com/hashicorp/serf/client
license: MPL-2.0, see serf-client-license.txt

google uuid, Copyright (c) 2009,2014 Google Inc.
https://pkg.go.dev/github.com/google/uuid
license: BSD-3-Clause, see google-uuid-license.txt

cobra library, Copyright 2013-2023 The Cobra Authors
https://pkg.go.dev/github.com/spf13/cobra
license: Apache-2.0, see cobra-license.txt


# VIRTX ARCHITECTURE

VirtX consists of two main parts, the virtxd service that needs to run on all KVM hosts in the cluster,
and the optional virtx command line client that uses the REST API to connect to the service (any of the hosts).

The virtx command line client can be installed on any remote client (a workstation or a laptop typically),
to control the cluster remotely, or it can be also used locally on any of the servers in the cluster.

On all KVM hosts, the serf service is also running, to provide the clustering layer.

A separate storage product or server is providing shared storage for the cluster.
In the current implementation, NFS and iSCSI have been considered and tested.
In theory any block device that maps to /dev/ should work, but specific IDs for SCSI and NVMe are recognized.

virtxd expects an NFSv4 directory

/vms

to be created and mounted on all the hosts of the cluster. It should be owned by qemu:qemu.

In the simplest configuration, this is sufficient for virtxd to then create all the subdirectories needed there:

/vms/reg  is the VM and host register. This is for the most part just managed by VirtX.
          When the cluster starts the first time, a number of subdirectories is created, one for every host
          in the cluster, if it does not exist already. The directory name is the host UUID.

          Host-specific OPTIONAL parameters ("options") are stored as regular files (one file per option)
          inside the host directory. Nothing is functionally required, but the user may want to configure
          certain parameters to optimize the cluster. These parameters are read once when virtxd starts.

          Current host options:

          lockid: this is the sanlock "host_id", used by sanlock to uniquely identify each host [1-2000].
                  Normally this value is dynamically generated when the cluster starts if not existing.

/vms/lock is where resource lock files are stored. Should only be looked at in case cleanup becomes necessary,
          but normally this is fully managed by VirtX.

/vms/ds   is meant to contain all NFS "datastores". Could be just part of the same /vms share, or it could be
          a separate single share, or it could contain multiple subdirectories, each a mountpoint for a different
          NFS "datastore". This is entirely up to the user / admin (or automation) to configure and replicate
          identically on all hosts.

/vms/gold is where to put golden images, for example ready-to-use cloud images. These are used during vm creation
          to initialize the OS disk with a full copy (potentially extended in size) of a cloud image.

For iSCSI storage (or potentially FC, not tested yet), virtxd currently assumes an existing iSCSI configuration
with LUNs (with multipath f.e.) already mapped to

/dev/...

The virtxd daemon has been tested running as qemu:qemu with supplementary groups "disk" and "sanlock".

virtxd monitors the state of local VMs via libvirt, and offers a REST API backend to connect to.
serf agent and libvirt must be already running when starting virtxd, or virtxd will not start successfully.
If the connection to libvirt or the serf agent are subsequently lost, virtx will attempt to reconnect every 5 seconds.


# LIBVIRT CONFIGURATION

libvirt on each host should be configured as follows:

```
/etc/libvirt/virtproxyd.conf:
listen_tls = 0
listen_tcp = 1
auth_tcp = "none"

/etc/libvirt/virtqemud.conf:
auth_unix_ro = "none"
auth_unix_rw = "none"

/etc/libvirt/qemu.conf:
user = "qemu"
group = "disk"
dynamic_ownership = 0
lock_manager = "sanlock"

/etc/libvirt/qemu-sanlock.conf:
auto_disk_leases = 0
require_lease_for_disks = 0
io_timeout = 0
user = "sanlock"
group = "sanlock"

/etc/sanlock/sanlock.conf: (for now, eventually we might enable watchdog):
use_watchdog = 0
```

---
Start sanlock

```
systemctl start sanlock
systemctl enable sanlock
```

Start the libvirt services as such:

```
systemctl start virtproxyd-tcp.socket virtqemud.socket
systemctl enable virtproxyd-tcp.socket virtqemud.socket
```

If you want to configure libvirt networks for your VM (recommended: bridged instead),
also start:

```
systemctl start virtnetworkd
systemctl enable virtnetworkd
```

# BUILD CLUSTER

Install and configure all hosts in the cluster like so.

Select an initial node to start the cluster (for example, "virt1"),
and start the serf agent using your distro-provided service or manually with:

```
serf agent &
```

start virtxd on the initial node, using the systemd provided service or f.e.: like so:

```
sudo -u qemu -g disk nohup virtxd
```

then proceed to the next node, where you will start the serf agent in the same way:

```
serf agent &
```

but then also run the serf command to join the initial node in the same cluster:

```
serf join virt1
```

These two commands can also be packed in a single command like so:

```
serf agent -join=virt1 &
```

after that, start virtxd on this node too, again with the same command:

```
sudo -u qemu -g disk nohup virtxd
```

If you are using systemd service files for serf and virtx, you will likely just
enable those services on all hosts, and on each host simply run a single command:

```
serf agent -join=virt1 &
```

Once all nodes have joined the cluster, you should be ready to test functionality
using the command line client

# COMMAND LINE CLIENT

There is auto-completion for the command line client. If it is not provided by
your distro package, you can extract it directly from the command line client. See:

```
virtx completion --help
```

If connecting from a remote host (not locally on one of the servers of the cluster),
be sure to export the env variable to be able to contact the REST API, for example:

```
export VIRTX_API_SERVER=virt1
```

as an alternative, you can provide the api server to use using the -A option.

See the command line help with

```
virtx --help
```

# TESTS

I would suggest a few tests to ensure the installation is ok:

```
virtx list host
```

all hosts in the cluster should be listed. This is the simplest test to ensure
that the basics work.

Create a VM adapting the examples provided in json/ starting with:

```
virtx create vm json/opensuse-15.5.json
virtx boot vm xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
```

and ensure that the new VM is visible with

```
virtx list vm
```

it might take a second or so for the new VM to appear in the list.


# STORAGE

Storage Management in VirtX is implicit with the lifecycle of VMS.
There are as of now no storage-specific APIs.

All storage is assumed to be shared storage, and it is also assumed that
the NFSv4 shares mounted into /vms/ds, as well as all the LUNs and mpath devices
visible as /dev/disk/by-id/... are already in place, mounted and visible from
all hosts in the cluster, and that the mount options are safe for VM storage operations
including live migration with sanlock.

The VirtX API for VM Creation includes in its definition all disks required by the VM,
and for each disk whether it is Managed and whether it is Provisioned by VirtX.

# MANAGED DISKS

"Managed" means that VirtX takes ownership of the referenced disk;
it will create a sanlock resource file for it with its LVB set to the VM uuid,

so that this disk will be used only when operating on this specific VM,
and any operation involving the disk will happen under a resource lease.
All this locking is _cooperative_, ie there is nothing preventing a process
on the host to trample over this mechanism.

At VM Deletion time, the REST client can explicitly requests the storage
associated with the VM to be deleted, and in that case the Managed virtual
disks will be deleted, and LUNs will be wiped.

# UNMANAGED DISKS

"Unmanaged" means that VirtX does not take ownership of the referenced disk;
it assumes that accessing it directly is ok. There might be some other locking
mechanism external to VirtX, or the resource might be ok to share.
At VM Deletion time, this disk will be untouched.

For example, you can use this to reference ISOs that can be shared between
multiple VMs as read-only CDROMS.
Use this option with care in all other cases, as it bypasses the cooperative
locking of disks entirely.

# PROVISIONED DISKS

"Managed" Disks (and _only_ managed disks) can be marked as "Provisioned".
A "Provisioned" disk will be created and prepared by VirtX for running
at VM Creation time (vm_create operation).

For virtual disks, it means that virtxd will use qemu-img to create a new image,
which can be a .qcow2 or a .raw image. The API allows for Thin-provisioned or Thick-provisioned virtual disks.

For LUNs, it means that virtxd will wipe the contents of the LUN at VM creation time.

By contrast, an "unprovisioned" disk will be assumed to be an existing resource.

# DEBUG ISSUES

Investigate issues using your journalctl (if running as service),
or get the standard error channel from the virtxd process if running manually.

# DEPENDENCIES


If you don't have serf already from the distro, get serf source code
(tested versions 0.8.2 to 0.10.2) from

https://github.com/hashicorp/serf

and you can just use

$ go build cmd/serf/main.go cmd/serf/commands.go

Then copy the resulting binary in your PATH somewhere:

$ sudo cp main /usr/local/bin/serf

Serf will be listening by default on port 7373 for the RPC user messages,
while it will listen on port 7946 (TCP and UDP) for serf itself.

The virtx API service will be listening on port 8080.

virtx-check-lvb is a small helper binary that virtxd uses to verify sanlock Lock Value Block (LVB)
ownership before executing operations on managed disks. It must be installed at:

/usr/sbin/virtx-check-lvb

It is built alongside virtxd from the same source tree (cmd/virtx-check-lvb/).

# TODO

- HA features are not implemented yet
- Minimal host selection algorithm (for HA)
- Golden images?

# BUGS

For some reason for me version go1.23.8 the old pre-1.22 net/http behavior is triggered,
and no 1.22+ API handler works. Arg.

To work around this, I have added in go.mod:

godebug (
    default=go1.23
)

if this does not work, an alternative is to use:

export GODEBUG="httpmuxgo121=0"

but for now the go.mod trick seems to work.

# CODE STYLE

Subject to change.

The "standard" code style for Golang is to have CamelCase everywhere which is a readability
disaster for me being used to C.

- **`snake_case`** for function names, variable names — not `camelCase`.
  Type names are CamelCase since code generators use that convention, and it would be too cumbersome to do otherwise.

- **Package-private symbols** get a short package prefix: e.g., `vm_list_loop` inside the `virtx` package.
- **Exported symbols** have an uppercase first letter but still use underscores: e.g., `vmdef.To_xml()`.
- **Type names** are CamelCase (required by code generators).
- `if`: always put parentheses around conditions
- no assignment and error checking on the same line
- Always put spaces around binary arithmetic operators. (a + b) is OK, but (a+b) is NOT.
- Struct literals have spaces inside braces: `Foo{ field1, field2 }` not `Foo{field1, field2}`.
