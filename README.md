# XXX EXPERIMENT/PROTOTYPE: DO NOT USE XXX

This is still very much experimental and likely insecure, buggy, needing code review.
Use at your own risk! You have been warned! Exclamation mark!

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
In the current implementation, only NFS has been implemented,
but some minimal iSCSI code has been recently added, reaching block devices mapped to /dev/

virtxd expects this directory to be mounted, from a remote NFS4 share:

/vms/xml

For iSCSI, virtxd currently assumes an existing iSCSI configuration with LUNs (with multipath f.e.)
already mapped to

/dev/...

For NFS4 storage, virtxd expects this additional directory to be mounted:

/vms/ds

In the simplest configuration with NFS, it could be a single /vms mountpoint.
Typically the mounted directories should be owned by qemu:qemu.

The virtxd daemon monitors the state of local VMs via libvirt, and offers a REST API backend to connect to.
serf agent and libvirt must be already running when starting virtxd, or virtxd will not start successfully.
If the connection to libvirt or the serf agent are subsequently lost, virtx will attempt to reconnect every 5 seconds.


# LIBVIRT CONFIGURATION

libvirt on each host should be configured as follows (pseudodiff):

--- /etc/libvirt/virtproxyd.conf    2025-12-01 14:57:32.000000000 -0700
+++ /etc/libvirt/virtproxyd.conf    2025-12-16 02:32:54.622835749 -0700
-#listen_tls = 0
+listen_tls = 0

-#listen_tcp = 1
+listen_tcp = 1

-#unix_sock_group = "libvirt"
+unix_sock_group = "qemu"

-#auth_unix_ro = "polkit"
+auth_unix_ro = "none"

-#auth_unix_rw = "polkit"
+auth_unix_rw = "none"

-#auth_tcp = "sasl"
+auth_tcp = "none"


/etc/libvirt/virtqemud.conf: same change as per virtproxyd above.

Start the libvirt services as such:

systemctl start virtlogd virtlockd virtproxyd virtqemud
systemctl enable virtlogd virtlockd virtproxyd virtqemud

If you want to configure libvirt networks for your VM (recommended: bridged instead),
also start:

systemctl start virtnetworkd
systemctl enable virtnetworkd

# BUILD CLUSTER

Install and configure all hosts in the cluster like so.

Select an initial node to start the cluster (for example, "virt1"),
and start the serf agent using your distro-provided service or manually with:

serf agent &

start virtxd on the initial node, using the systemd provided service or f.e.: like so:

sudo -u qemu -g qemu nohup virtxd

then proceed to the next node, where you will start the serf agent in the same way:

serf agent &

but then also run the serf command to join the initial node in the same cluster:

serf join virt1

These two commands can also be packed in a single command like so:

serf agent -join=virt1 &

after that, start virtxd on this node too, again with the same command:

sudo -u qemu -g qemu nohup virtxd

If you are using systemd service files for serf and virtx, you will likely just
enable those services on all hosts, and on each host simply run a single command:

serf agent -join=virt1 &

Once all nodes have joined the cluster, you should be ready to test functionality
using the command line client

# COMMAND LINE CLIENT

There is auto-completion for the command line client. If it is not provided by
your distro package, you can extract it directly from the command line client. See:

virtx completion --help

Using --help, the command line client should be discoverable.

If connecting from a remote host (not locally on one of the servers of the cluster),
be sure to export the env variable to be able to contact the REST API, for example:

export VIRTX_API_SERVER=virt1

as an alternative, you can provide the api server to use using the -A option.

# TESTS

I would suggest 3 tests to ensure the installation is ok:

virtx list host

all hosts in the cluster should be listed. This is the simplest test to ensure

Create a VM adapting the examples provided in json/ starting with:

virtx create vm json/opensuse-15.5.json
virtx boot vm xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx


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

# TODO

- migration (offline/live) needs more testing and probably changes
- some testing should be done with an HTTPS proxy in front (nginx?)
- Only NFS is implemented as shared storage (no iSCSI)
- HA features are not implemented yet
- ...a lot more

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

So in most places (function names, variable names, struct fields etc),
I used instead the convention of using _.

For symbols only visible within the package, I use a short prefix for the package.
For symbols exported, the first letter needs to be upper case, and I do not prepend the package prefix.

This way, within the package, package-private symbols will look like this:

vmdef_disk_to_xml()

and global ones will look like this:

import (
 .../vmdef
)

vmdef.To_xml()

Type names are CamelCase since code generators use that convention, and it would be too cumbersome to do otherwise.
