.PHONY: all clean

all: virtXD

virtXD:
	go build -gcflags="-l -m"

clean:
	go clean
