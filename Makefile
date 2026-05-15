# Makefile for kube-time-machine.
# Keep targets minimal — every line must be defensible. Add only when needed.

GO         ?= go
BIN_DIR    := bin
AGENT_BIN  := $(BIN_DIR)/ktm-agent
CLI_BIN    := $(BIN_DIR)/ktm

.PHONY: help build agent cli test fmt vet tidy clean

help:
	@echo "make build   — build both binaries into ./bin"
	@echo "make agent   — build only the in-cluster agent"
	@echo "make cli     — build only the local CLI"
	@echo "make test    — run all unit tests"
	@echo "make fmt     — gofmt on the whole tree"
	@echo "make vet     — go vet on the whole tree"
	@echo "make tidy    — go mod tidy"
	@echo "make clean   — remove ./bin"

build: agent cli

agent:
	$(GO) build -o $(AGENT_BIN) ./cmd/agent

cli:
	$(GO) build -o $(CLI_BIN) ./cmd/ktm

test:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

tidy:
	$(GO) mod tidy

clean:
	rm -rf $(BIN_DIR)
