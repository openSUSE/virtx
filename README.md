# inventory-service

A service providing VM inventory data.

The service monitors the state of local VMs via libvirt and transmits
inventory data to other nodes using a serf agent as transport. The same
way, it receives events and keeps a local in-memory copy of the complete
cluster state. The service exposes an HTTP endpoint serving inventory
data.
