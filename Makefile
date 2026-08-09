BINARY  := alpaca
PKG     := ./cmd/alpaca
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Platforms worth shipping: the point of a single static binary is that copying
# it to another machine is the whole deployment step.
PLATFORMS := \
	linux/amd64 \
	linux/arm64 \
	darwin/amd64 \
	darwin/arm64 \
	windows/amd64

.PHONY: all build install test race lint fmt cross clean run

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)

install:
	go install -ldflags "$(LDFLAGS)" $(PKG)

test:
	go test ./... -race -timeout 300s

# Exercises the paths that need a real ollama daemon and a real router.
test-live:
	ALPACA_LIVE=1 go test ./... -race -timeout 600s -v

lint:
	go vet ./...
	@gofmt -l . | grep . && { echo "gofmt needed on the files above"; exit 1; } || echo "gofmt clean"

fmt:
	gofmt -w .

cross:
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=dist/$(BINARY)-$$os-$$arch; \
		if [ "$$os" = "windows" ]; then out=$$out.exe; fi; \
		echo "  $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
			go build -ldflags "$(LDFLAGS)" -o $$out $(PKG) || exit 1; \
	done
	@echo
	@ls -lh dist/

run: build
	./$(BINARY) serve

clean:
	rm -rf $(BINARY) dist/
