.PHONY: all clean

all: virtXD

SRC = $(shell find . -name "*.go")

virtXD: $(SRC)
	go build -gcflags="-N -l -m"

clean:
	go clean
