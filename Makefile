# CCattler Makefile — build targets for local development and CI.
#
# Standard build (no optional data planes):
#   make build
#
# Build with eBPF data plane (requires compiled BPF ELF):
#   make build-ebpf
#
# Compile BPF via Docker (works on macOS — no clang needed):
#   make bpf
#
# Full eBPF pipeline (compile BPF + build Go binary):
#   make ebpf-full
#
# Cross-compile for Linux (from macOS):
#   make linux
#   make linux-ebpf

BINARY    := cca
BUILD_DIR := ./cmd/cca
OUTPUT    := $(BINARY)

# BPF build settings.
BPF_SOURCE     := network/ebpf/cca_dataplane.c
BPF_OUTPUT     := network/ebpf/cca_dataplane.o
BPF_IMAGE      := cca-bpf-builder
BPF_DOCKERFILE := scripts/Dockerfile.bpf

.PHONY: build build-ebpf build-cilium linux linux-ebpf \
        bpf bpf-image ebpf-full \
        test lint fmt deploy-ebpf clean help

## Build targets

build:  ## Build cca binary (standard, no optional data planes)
	go build -o $(OUTPUT) $(BUILD_DIR)

build-ebpf:  ## Build cca with embedded eBPF data plane
	go build -tags ebpf -o $(OUTPUT) $(BUILD_DIR)

build-cilium:  ## Build cca with Cilium eBPF data plane
	go build -tags cilium -o $(OUTPUT) $(BUILD_DIR)

linux:  ## Cross-compile for Linux/amd64 (standard)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $(OUTPUT)-linux $(BUILD_DIR)

linux-ebpf:  ## Cross-compile for Linux/amd64 with embedded eBPF
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags ebpf -o $(OUTPUT)-linux-ebpf $(BUILD_DIR)

## BPF compilation (Docker — works on macOS and Linux)

bpf-image:  ## Build the BPF cross-compiler Docker image (one-time)
	docker build --platform linux/amd64 -t $(BPF_IMAGE) -f $(BPF_DOCKERFILE) .

bpf: bpf-image  ## Compile unified BPF data plane ELF via Docker
	docker run --rm --platform linux/amd64 \
	  -v $(CURDIR)/network/ebpf:/src \
	  $(BPF_IMAGE) \
	  -c /src/cca_dataplane.c -o /src/cca_dataplane.o
	@echo "BPF compiled: $(BPF_OUTPUT)"

ebpf-full: bpf build-ebpf  ## Full pipeline: compile BPF + build Go binary
	@echo "eBPF build complete: $(OUTPUT) with embedded BPF"

## Quality

test:  ## Run all tests
	go test ./...

lint:  ## Run golangci-lint
	golangci-lint run ./...

fmt:  ## Format all Go and .cca files
	gofmt -w .
	find . -name '*.cca' -exec ./$(OUTPUT) fmt {} \; 2>/dev/null || true

## Deploy (bare-metal testbed)

deploy-ebpf:  ## Build and install on testbed node-1 (192.168.100.215)
	@echo "Cross-compiling for Linux with embedded BPF..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags ebpf -o /tmp/cca-linux-ebpf $(BUILD_DIR)
	@echo "Copying binary to node-1..."
	scp -i ~/.ssh/id_rsa_dell_bozhan /tmp/cca-linux-ebpf bozhan@192.168.100.215:/tmp/cca-ebpf-bin
	@echo "Installing on node-1..."
	ssh -i ~/.ssh/id_rsa_dell_bozhan bozhan@192.168.100.215 \
	  "sudo systemctl stop cca-agent cca-server 2>/dev/null; \
	   sudo cp /tmp/cca-ebpf-bin /usr/local/bin/cca && \
	   sudo systemctl start cca-server cca-agent 2>/dev/null; \
	   echo 'Installed: cca binary with embedded BPF on node-1'"

## Housekeeping

clean:  ## Remove build artifacts
	rm -f $(OUTPUT) $(OUTPUT)-linux $(OUTPUT)-linux-ebpf
	rm -f /tmp/cca-linux-ebpf /tmp/cca-ebpf-bin

help:  ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
