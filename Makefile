IMAGE = inventory-service:devel

GOFILES ?= $(shell go list ./...)

default: build

image:
	@echo "Building image: $(IMAGE)"
	@docker build -t $(IMAGE) -f images/inventory/Dockerfile .

run: image
	@echo "Using image: $(IMAGE)"
	@docker run --rm -ti -v /var/run/libvirt:/var/run/libvirt:ro -p 8080:8080/tcp $(IMAGE)

build: format
	@echo Running go build
	@go build

format:
	@echo "Running go fmt"
	@go fmt $(GOFILES)

.PHONY: image format
