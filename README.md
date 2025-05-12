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

Adding to the go.mod a section that seems to fix it, hopefully this will work.

# CODE STYLE

The "standard" code style for Golang is to have CamelCase everywhere which is a readability
disaster for me being used to C (and probably one of the worst style choices of Go).

So in most places (function names, variable names, struct fields etc),
I used instead the convention of using _ as it should be.

The first letter case expresses the visibility of the symbol, which is even more readable this way.

I made an exception for types: type names are CamelCase since code generators use that convention,
and it would be too cumbersome to do otherwise.
