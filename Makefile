SHELL := /bin/bash
GO ?= go
GO_CMD := CGO_ENABLED=0 $(GO)
GIT_COMMIT := $(shell git rev-parse HEAD)
DOCKER_REPO ?= ghcr.io/xperimental/hugo-preview
DOCKER_TAG ?= dev

.PHONY: all
all: test build-binary

.PHONY: test
test:
	$(GO_CMD) test -cover ./...

.PHONY: build-binary
build-binary:
	$(GO_CMD) build -tags netgo -ldflags "-w -X main.GitCommit=$(GIT_COMMIT)" -o hugo-preview .

.PHONY: image
image:
	docker buildx build -t "$(DOCKER_REPO):$(DOCKER_TAG)" --load .

.PHONY: clean
clean:
	rm -f hugo-preview
