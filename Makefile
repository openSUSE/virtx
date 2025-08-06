.PHONY: all clean

all: virtxd virtx

GO_BUILD=go build -gcflags="-N -l -m"
PKG_SRC=$(shell find pkg/ -name "*.go")

virtxd: $(PKG_SRC) ./cmd/virtxd
	$(GO_BUILD) -o $@ ./cmd/virtxd

virtx: $(PKG_SRC) cmd/virtx
	$(GO_BUILD) -o $@ ./cmd/virtx

clean:
	rm -f virtxd virtx
