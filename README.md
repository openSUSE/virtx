# virtXD

The service monitors the state of local VMs via libvirt.

It has been derived from the inventory-service, which will be replaced
by an API backend.

Note that to run virtXD, serf must be already running.

get serf source code (tested version 0.8.2) from

https://github.com/hashicorp/serf

and you can just use

$ go build cmd/serf/main.go cmd/serf/commands.go

Then copy the resulting binary in your PATH:

$ sudo cp main /usr/local/bin/serf

Serf will be listening by default on port 7070,
while the API service will be listening on port 8080.
