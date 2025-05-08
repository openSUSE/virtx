# LICENSE

The code is under the GPLv2 license. See LICENSE file for details.

# virtXD

The service monitors the state of local VMs via libvirt.

It has been derived from the inventory-service, which will be replaced by an API backend.
Note that to run virtXD, serf must be already running.

get serf source code (tested version 0.8.2) from

https://github.com/hashicorp/serf

and you can just use

$ go build cmd/serf/main.go cmd/serf/commands.go

Then copy the resulting binary in your PATH:

$ sudo cp main /usr/local/bin/serf

Serf will be listening by default on port 7373 for the RPC user messages,
while it will listen on port 7946 (TCP and UDP) for serf itself.

while the API service will be listening on port 8080.

# BUGS

export GODEBUG="httpmuxgo121=0"

seems necessary before starting virtxd, otherwise the old pre-1.22 go behavior is triggered,
and no API handler works. Arg.
