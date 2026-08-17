BINARY  := alpaca
PKG     := ./cmd/alpaca

# Only release tags describe a version. Matching v* keeps feature tags like
# feat/web-search out of `alpaca version`, falling back to a short commit sha.
VERSION := $(shell git describe --tags --match 'v[0-9]*' --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Where `make install` puts the binary.
#
# Picked at build time rather than hard-coded, because an install location that
# is not already on PATH does not achieve the one thing install is for. The
# first candidate that is both writable and on PATH wins; /usr/local/bin is the
# last resort and is the only one that typically needs sudo. Override with
# `make install BINDIR=/somewhere/else`.
#
# Written without a `case` statement on purpose: make counts parentheses inside
# $(shell ...), so the ) closing a case pattern terminates the expansion early.
BINDIR ?= $(shell \
	gobin="$$(go env GOBIN)"; \
	if [ -n "$$gobin" ]; then echo "$$gobin"; exit 0; fi; \
	for d in "$$(go env GOPATH)/bin" "$$HOME/.local/bin" /usr/local/bin; do \
		if [ -w "$$d" ] && printf '%s' ":$$PATH:" | grep -qF ":$$d:"; then \
			echo "$$d"; exit 0; \
		fi; \
	done; \
	echo /usr/local/bin)

# Platforms worth shipping: the point of a single static binary is that copying
# it to another machine is the whole deployment step.
PLATFORMS := \
	linux/amd64 \
	linux/arm64 \
	darwin/amd64 \
	darwin/arm64 \
	windows/amd64

# Where the user's Emacs configuration lives: the XDG location when it is in
# use, the classic ~/.emacs.d otherwise. The autoload block lands in ~/.emacs
# when that file exists (Emacs reads it in preference to init.el), else in the
# config dir's init.el, created if missing.
EMACSDIR  ?= $(shell if [ -d "$$HOME/.config/emacs" ]; then \
		echo "$$HOME/.config/emacs"; else echo "$$HOME/.emacs.d"; fi)
EMACSINIT ?= $(shell if [ -f "$$HOME/.emacs" ]; then \
		echo "$$HOME/.emacs"; else echo "$(EMACSDIR)/init.el"; fi)

.PHONY: all build install update uninstall where test test-live lint fmt cross clean run \
	emacs emacs-install emacs-uninstall

# The golden path: a bare `make` leaves `alpaca` runnable by name from any
# directory. There is no daemon to launch on this machine — serving happens
# wherever `alpaca serve` runs — so build-and-install is the whole journey.
all: install

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) $(PKG)
	@echo "built ./$(BINARY) ($(VERSION))"

# Installs the binary built by `build`, rather than `go install` recompiling its
# own copy, so what lands on PATH is exactly what was just tested.
install: build
	@if [ ! -d "$(BINDIR)" ]; then \
		mkdir -p "$(BINDIR)" 2>/dev/null || sudo mkdir -p "$(BINDIR)"; \
	fi
	@if [ -w "$(BINDIR)" ]; then \
		install -m 0755 $(BINARY) "$(BINDIR)/$(BINARY)"; \
	else \
		echo "$(BINDIR) is not writable, using sudo"; \
		sudo install -m 0755 $(BINARY) "$(BINDIR)/$(BINARY)"; \
	fi
	@echo "installed $(BINDIR)/$(BINARY)"
	@resolved=$$(command -v $(BINARY) 2>/dev/null); \
	if [ -z "$$resolved" ]; then \
		echo; \
		echo "warning: $(BINDIR) is not on your PATH, so \`$(BINARY)\` still will not resolve."; \
		echo "  add this to your shell profile:"; \
		echo "    export PATH=\"$(BINDIR):\$$PATH\""; \
	elif [ "$$resolved" != "$(BINDIR)/$(BINARY)" ]; then \
		echo; \
		echo "warning: \`$(BINARY)\` resolves to $$resolved, which shadows the copy just installed."; \
		echo "  remove that one, or put $(BINDIR) earlier in PATH."; \
	else \
		echo "run it from anywhere:  $(BINARY) serve"; \
	fi

# Full refresh of a live install: stop anything running the old binary,
# remove it, then build and install fresh. Chat sessions are interactive and
# cannot be resurrected, so unlike a daemon there is nothing to restart —
# rerun `alpaca chat` (or `alpaca serve`) afterwards.
update:
	-pkill -x $(BINARY) 2>/dev/null || true
	@if [ -w "$(BINDIR)" ]; then rm -f "$(BINDIR)/$(BINARY)"; \
	else sudo rm -f "$(BINDIR)/$(BINARY)"; fi
	@$(MAKE) --no-print-directory install

uninstall:
	@if [ -w "$(BINDIR)" ]; then rm -f "$(BINDIR)/$(BINARY)"; \
	else sudo rm -f "$(BINDIR)/$(BINARY)"; fi
	@echo "removed $(BINDIR)/$(BINARY)"

# Shows where install would put things, without touching anything.
where:
	@echo "version  $(VERSION)"
	@echo "bindir   $(BINDIR)"
	@printf "on PATH  "; if printf '%s' ":$$PATH:" | grep -qF ":$(BINDIR):"; \
		then echo yes; else echo "no — add it to your shell profile"; fi
	@printf "current  "; command -v $(BINARY) 2>/dev/null || echo "not installed"

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

# M-x alpaca inside Emacs. Installs the binary first — the Emacs commands run
# it by name — then copies the package into the configuration and appends a
# marked autoload block to the init file, exactly once. `make emacs` is an
# alias, so `make emacs install` also lands somewhere sensible.
emacs: emacs-install

emacs-install: install
	@mkdir -p "$(EMACSDIR)/site-lisp/alpaca"
	@install -m 0644 emacs/alpaca.el "$(EMACSDIR)/site-lisp/alpaca/alpaca.el"
	@echo "installed $(EMACSDIR)/site-lisp/alpaca/alpaca.el"
	@mkdir -p "$$(dirname "$(EMACSINIT)")" && touch "$(EMACSINIT)"
	@if grep -q '>>> alpaca' "$(EMACSINIT)"; then \
		echo "autoloads already wired into $(EMACSINIT)"; \
	else \
		{ echo ''; \
		  echo ';; >>> alpaca (managed by make emacs-install / make emacs-uninstall)'; \
		  echo '(add-to-list (quote load-path) "$(EMACSDIR)/site-lisp/alpaca")'; \
		  echo '(autoload (quote alpaca) "alpaca" "Open the alpaca chat TUI." t)'; \
		  echo '(autoload (quote alpaca-demo) "alpaca" "Open the alpaca chat TUI in demo mode." t)'; \
		  echo '(autoload (quote alpaca-serve) "alpaca" "Run alpaca serve in a buffer." t)'; \
		  echo ';; <<< alpaca'; \
		} >> "$(EMACSINIT)"; \
		echo "wired autoloads into $(EMACSINIT)"; \
	fi
	@echo "M-x alpaca opens the chat — restart Emacs (or eval the new init block) first"

emacs-uninstall:
	rm -rf "$(EMACSDIR)/site-lisp/alpaca"
	@if [ -f "$(EMACSINIT)" ] && grep -q '>>> alpaca' "$(EMACSINIT)"; then \
		perl -ni -e 'print unless />>> alpaca/ .. /<<< alpaca/' "$(EMACSINIT)"; \
		echo "removed the alpaca block from $(EMACSINIT)"; \
	fi

run: build
	./$(BINARY) serve

clean:
	rm -rf $(BINARY) dist/
