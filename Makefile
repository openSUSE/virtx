.PHONY: all clean

all: virtxd virtxc

GO_BUILD=go build -gcflags="-N -l -m"
PKG_SRC=$(shell find pkg/ -name "*.go")

virtxd: $(PKG_SRC) ./cmd/virtxd
	$(GO_BUILD) -o $@ ./cmd/virtxd

virtxc: $(PKG_SRC) cmd/virtxc
	$(GO_BUILD) -o $@ ./cmd/virtxc

clean:
	rm -f virtxd virtxc
