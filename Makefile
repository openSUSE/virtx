all: virtXD

virtXD:
	go build -gcflags="-l -m"
