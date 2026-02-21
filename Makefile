.PHONY: all clean

all: virtxd virtx virtx-check-lvb

PKG_SRC=$(shell find pkg/ -name "*.go")
VERSION=$(shell git describe --tags --always --dirty)
GO_BUILD=go build -gcflags="-N -l -m" -ldflags "-X main.version=$(VERSION)"

virtxd: $(PKG_SRC) ./cmd/virtxd
	$(GO_BUILD) -o $@ ./cmd/virtxd

virtx: $(PKG_SRC) ./cmd/virtx
	$(GO_BUILD) -o $@ ./cmd/virtx

virtx-check-lvb: ./cmd/virtx-check-lvb
	$(GO_BUILD) -o $@ ./cmd/virtx-check-lvb

clean:
	rm -f virtxd virtx virtx-check-lvb
