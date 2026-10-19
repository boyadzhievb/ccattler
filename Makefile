# CCattler Makefile — build targets for local development and CI.
#
# Standard build (no optional data planes):
#   make build
#
# Build with eBPF data plane (requires cilium/ebpf Go module):
#   make build-ebpf
#
# Cross-compile for Linux (from macOS):
#   make linux
#   make linux-ebpf
#
# Compile BPF C programs on a Linux host:
#   make bpf
#
# Full eBPF pipeline (compile BPF + build Go binary):
#   make ebpf-full    # on Linux only

BINARY    := cca
BUILD_DIR := ./cmd/cca
OUTPUT    := $(BINARY)

# BPF build settings.
BPF_SOCKET_LB_SOURCE := network/ebpf/cca_socket_lb.c
BPF_TC_DNAT_SOURCE   := network/ebpf/cca_tc_dnat.c
BPF_OUTPUT_DIR       := /usr/lib/cca
BPF_CC               := clang
BPF_CFLAGS           := -O2 -g -Wall -Werror -target bpf -D__TARGET_ARCH_x86
BPF_INCLUDES         := -I/usr/include -I/usr/include/x86_64-linux-gnu

.PHONY: build build-ebpf build-cilium linux linux-ebpf bpf ebpf-full \
        test lint fmt clean help

## Build targets

build:  ## Build cca binary (standard, no optional data planes)
	go build -o $(OUTPUT) $(BUILD_DIR)

build-ebpf:  ## Build cca with independent eBPF data plane
	go build -tags ebpf -o $(OUTPUT) $(BUILD_DIR)

build-cilium:  ## Build cca with Cilium eBPF data plane
	go build -tags cilium -o $(OUTPUT) $(BUILD_DIR)

linux:  ## Cross-compile for Linux/amd64 (standard)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $(OUTPUT)-linux $(BUILD_DIR)

linux-ebpf:  ## Cross-compile for Linux/amd64 with eBPF data plane
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags ebpf -o $(OUTPUT)-linux-ebpf $(BUILD_DIR)

## BPF compilation (Linux only)

bpf:  ## Compile BPF C programs to ELF (requires clang + libbpf-dev)
	@if [ "$$(uname)" != "Linux" ]; then echo "error: BPF compilation requires Linux"; exit 1; fi
	sudo mkdir -p $(BPF_OUTPUT_DIR)
	$(BPF_CC) $(BPF_CFLAGS) $(BPF_INCLUDES) -c $(BPF_SOCKET_LB_SOURCE) -o $(BPF_OUTPUT_DIR)/cca_ebpf.o
	$(BPF_CC) $(BPF_CFLAGS) $(BPF_INCLUDES) -c $(BPF_TC_DNAT_SOURCE) -o $(BPF_OUTPUT_DIR)/cca_tc_dnat.o
	@echo "BPF compiled: $(BPF_OUTPUT_DIR)/cca_ebpf.o + cca_tc_dnat.o"

bpf-local:  ## Compile BPF to local directory (no sudo needed)
	@if [ "$$(uname)" != "Linux" ]; then echo "error: BPF compilation requires Linux"; exit 1; fi
	$(BPF_CC) $(BPF_CFLAGS) $(BPF_INCLUDES) -c $(BPF_SOCKET_LB_SOURCE) -o cca_ebpf.o
	$(BPF_CC) $(BPF_CFLAGS) $(BPF_INCLUDES) -c $(BPF_TC_DNAT_SOURCE) -o cca_tc_dnat.o
	@echo "BPF compiled: cca_ebpf.o + cca_tc_dnat.o"

ebpf-full: bpf build-ebpf  ## Full eBPF pipeline: compile BPF + build Go binary (Linux only)
	@echo "eBPF build complete: $(OUTPUT) + $(BPF_OUTPUT)"

## Quality

test:  ## Run all tests
	go test ./...

lint:  ## Run golangci-lint
	golangci-lint run ./...

fmt:  ## Format all Go and .cca files
	gofmt -w .
	find . -name '*.cca' -exec ./$(OUTPUT) fmt {} \; 2>/dev/null || true

## Deploy (bare-metal testbed)

deploy-ebpf:  ## Build, copy, and install on testbed node-1 (192.168.100.215)
	@echo "Cross-compiling for Linux..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags ebpf -o /tmp/cca-linux-ebpf $(BUILD_DIR)
	@echo "Copying binary and BPF source to node-1..."
	scp -i ~/.ssh/id_rsa_dell_bozhan /tmp/cca-linux-ebpf bozhan@192.168.100.215:/tmp/cca-ebpf-bin
	rsync -avz -e "ssh -i ~/.ssh/id_rsa_dell_bozhan" network/ebpf/ bozhan@192.168.100.215:/tmp/cca-ebpf/
	@echo "Compiling BPF on node-1..."
	ssh -i ~/.ssh/id_rsa_dell_bozhan bozhan@192.168.100.215 \
	  "$(BPF_CC) $(BPF_CFLAGS) $(BPF_INCLUDES) -c /tmp/cca-ebpf/cca_socket_lb.c -o /tmp/cca-ebpf/cca_ebpf.o && \
	   $(BPF_CC) $(BPF_CFLAGS) $(BPF_INCLUDES) -c /tmp/cca-ebpf/cca_tc_dnat.c -o /tmp/cca-ebpf/cca_tc_dnat.o && \
	   sudo mkdir -p $(BPF_OUTPUT_DIR) && \
	   sudo cp /tmp/cca-ebpf/cca_ebpf.o $(BPF_OUTPUT_DIR)/cca_ebpf.o && \
	   sudo cp /tmp/cca-ebpf/cca_tc_dnat.o $(BPF_OUTPUT_DIR)/cca_tc_dnat.o && \
	   sudo cp /tmp/cca-ebpf-bin /usr/local/bin/cca && \
	   echo 'Installed: cca binary + 2 BPF ELFs on node-1'"

## Housekeeping

clean:  ## Remove build artifacts
	rm -f $(OUTPUT) $(OUTPUT)-linux $(OUTPUT)-linux-ebpf cca_ebpf.o cca_tc_dnat.o
	rm -f /tmp/cca-linux-ebpf /tmp/cca-ebpf-bin

help:  ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*##' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
