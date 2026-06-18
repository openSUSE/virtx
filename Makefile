.PHONY: all clean check build-tests

PREFIX  ?= /usr
SBINDIR ?= $(PREFIX)/sbin

PKG_SRC=$(shell find pkg/ -name "*.go")
VERSION=$(shell git describe --tags --always --dirty)
LDFLAGS := \
    -X main.version=$(VERSION) \
    -X 'suse.com/virtx/pkg/constants.VIRTX_CHECK_LVB=$(SBINDIR)/virtx-check-lvb'
GO_BUILD=go build -gcflags="-N -l -m" -ldflags "$(LDFLAGS)"

all: virtxd virtx virtx-check-lvb

virtxd: $(PKG_SRC) ./cmd/virtxd
	$(GO_BUILD) -o $@ ./cmd/virtxd

virtx: $(PKG_SRC) ./cmd/virtx
	$(GO_BUILD) -o $@ ./cmd/virtx

virtx-check-lvb: ./cmd/virtx-check-lvb
	$(GO_BUILD) -o $@ ./cmd/virtx-check-lvb

build-tests:
	for PKG in `go list ./...`; do \
		NAME=`echo $$PKG | tr '/' '_'`; \
		go test -c -o $${NAME}.test $${PKG}; \
	done

check: build-tests
	failed=0; \
	for TEST in *.test; do \
		echo "=== Running $${TEST} ==="; \
		./$${TEST} -test.v || failed=$$((failed + 1)); \
	done; \
	echo "=========================================="; \
	echo ""; \
	result=PASS; exitcode=0; \
	if [ $${failed} -gt 0 ]; then result=FAIL; exitcode=1; fi; \
	echo "$${result} ($${failed} tests failed)"; \
	exit $${exitcode}

clean:
	rm -f virtxd virtx virtx-check-lvb
