# LICENSE

The code is under the GPLv2 license. See LICENSE file for details.

# virtxd

The service monitors the state of local VMs via libvirt,
and offers a REST API backend to connect to.

serf agent and libvirt must be already running when starting virtxd.
If libvirt disconnects, virtx will attempt to reconnect,
while for now a serf agent failure will make virtx quit. (TODO)

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

For some reason on OpenSUSE Go version go1.23.8 the old pre-1.22 net/http behavior is triggered,
and no 1.22+ API handler works. Arg.

To work around this, I have added in go.mod:

godebug (
    default=go1.23
)

if this does not work, an alternative is to use:

export GODEBUG="httpmuxgo121=0"

but for now the go.mod trick seems to work.

# CODE STYLE

The "standard" code style for Golang is to have CamelCase everywhere which is a readability
disaster for me being used to C (and probably one of the worst style choices of Go IMO).

So in most places (function names, variable names, struct fields etc),
I used instead the convention of using _ as it should be.

The first letter case expresses the visibility of the symbol (argh).

I made an exception for types: type names are CamelCase since code generators use that convention,
and it would be too cumbersome to do otherwise.
