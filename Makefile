BINARY  := barghvim
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= $(BINARY):$(VERSION)

.DEFAULT_GOAL := help

## help: list the available targets
help:
	@awk 'BEGIN {FS = ": "} /^## / {sub(/^## /, ""); printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## build: compile the server into ./bin
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o bin/$(BINARY) .

## run: run the server locally
run:
	go run .

## test: run the test suite with the race detector
test:
	go test -race ./...

## cover: run the tests and open the coverage report
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

# Tools are pinned in go.mod under the `tool` directive and run with `go tool`.
# Nothing needs installing, and every machine gets the same version.
# `go get -tool <pkg>@<ver>` adds one.

## vet: run go vet
vet:
	go vet ./...

## modernize: report code that predates a newer stdlib idiom
# Keyed on the exit status, not on whether anything was printed: modernize
# writes findings to stderr and exits non-zero, but `go tool` also writes
# "go: downloading ..." there on a cold module cache. Capturing the output and
# testing it for emptiness reports those downloads as lint findings, which is
# invisible locally with a warm cache and fails every clean CI run.
modernize:
	@if go tool modernize ./...; then \
		echo "modernize: clean"; \
	else \
		echo; \
		echo "run 'make modernize-fix' to apply these"; \
		exit 1; \
	fi

## modernize-fix: apply modernize's suggestions
modernize-fix:
	go tool modernize -fix ./...

## lint: vet + modernize
lint: vet modernize

## fmt: format the source tree
fmt:
	gofmt -w .

## tidy: prune and verify module requirements
tidy:
	go mod tidy
	go mod verify

## tools: list the tool dependencies pinned in go.mod
tools:
	@awk '/^tool \(/{f=1;next} /^tool /{print "  " $$2} /^\)/{f=0} f && NF{print "  " $$1}' go.mod
	@echo
	@echo "  add one:  go get -tool <package>@<version>"
	@echo "  run one:  go tool <name>"

## check: everything CI would run
check: fmt lint test

## image: build the container image
image:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

## serve: run the container image on port 8080
serve: image
	docker run --rm -p 8080:8080 --env-file .env $(IMAGE)

## clean: remove build output
clean:
	rm -rf bin coverage.out

.PHONY: help build run test cover vet modernize modernize-fix lint fmt tidy tools check image serve clean
