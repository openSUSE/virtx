SERF_IMAGE = test-serf:devel

GOFILES ?= $(shell go list ./...)

default: build

image:
	@echo "Building image: $(SERF_IMAGE)"
	@docker build -t $(SERF_IMAGE) -f images/serf/Dockerfile .

build: format
	@echo Running go build
	@go build

format:
	@echo "Running go fmt"
	@go fmt $(GOFILES)

.PHONY: image format
