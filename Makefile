MODULE := github.com/boyadzhievb/ccattler
BINARIES := cca mcp

.PHONY: build test test-race lint bench fuzz clean

build:
	@for binary in $(BINARIES); do \
		echo "building $$binary"; \
		go build -o bin/$$binary ./cmd/$$binary; \
	done

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

bench:
	go test -bench=. -benchmem -run=^$$ ./...

fuzz:
	go test -fuzz=Fuzz -fuzztime=30s ./lang/...

clean:
	rm -rf bin/
