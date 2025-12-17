# XXX EXPERIMENT/PROTOTYPE: DO NOT USE XXX

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

# virtxd

The service monitors the state of local VMs via libvirt, and offers a REST API backend to connect to.
serf agent and libvirt must be already running when starting virtxd, or virtxd will not start successfully.
If the connection to libvirt or the serf agent are subsequently lost, virtx will attempt to reconnect every 5 seconds.

get serf source code (tested versions 0.8.2 to 0.10.2) from

https://github.com/hashicorp/serf

and you can just use

$ go build cmd/serf/main.go cmd/serf/commands.go

Then copy the resulting binary in your PATH:

$ sudo cp main /usr/local/bin/serf

Serf will be listening by default on port 7373 for the RPC user messages,
while it will listen on port 7946 (TCP and UDP) for serf itself.

The virtx API service will be listening on port 8080.

# TODO

- migration (offline/live) needs more testing and probably changes
- security is completely missing, implementation is just plain HTTP and libvirt TCP. No API keys, certificates etc.
- Only NFS is implemented as shared storage (no iSCSI)
- HA features are not implemented yet
- ...

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
