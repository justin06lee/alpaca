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

# The desktop app: the same binary, wrapped for whichever desktop is here. A
# bundle on macOS, a .desktop entry on Linux; both launch it with no terminal
# attached, which is what routes the bare binary into gui mode.
APP     := Alpaca
APPDIR  := build/$(APP).app
UNAME_S := $(shell uname -s)

# Linux desktop entry and icon, per-user so no target needs root.
XDG_APPS  := $(HOME)/.local/share/applications
XDG_ICONS := $(HOME)/.local/share/icons/hicolor

.PHONY: all build install update uninstall where test test-live lint fmt cross clean run \
	app app-install icns desktop desktop-install

# The golden path: a bare `make` leaves `alpaca` runnable by name from any
# directory — and the desktop app installed for the desktop in front of you:
# $(APP).app in /Applications on macOS, a launcher entry on Linux. There is no
# daemon to launch on this machine — serving happens wherever `alpaca serve`
# runs — so building and installing is the whole journey.
ifeq ($(UNAME_S),Darwin)
all: install app-install
else ifeq ($(UNAME_S),Linux)
all: install desktop-install
else
all: install
endif

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
# remove it (the app bundle included), then build and install fresh. Chat
# sessions are interactive and cannot be resurrected, so unlike a daemon
# there is nothing to restart — rerun `alpaca chat` (or reopen the app).
update:
	-pkill -x $(BINARY) 2>/dev/null || true
	@if [ -w "$(BINDIR)" ]; then rm -f "$(BINDIR)/$(BINARY)"; \
	else sudo rm -f "$(BINDIR)/$(BINARY)"; fi
	@$(MAKE) --no-print-directory all

uninstall:
	@if [ -w "$(BINDIR)" ]; then rm -f "$(BINDIR)/$(BINARY)"; \
	else sudo rm -f "$(BINDIR)/$(BINARY)"; fi
	@echo "removed $(BINDIR)/$(BINARY)"
	@if [ "$(UNAME_S)" = "Darwin" ] && [ -d "/Applications/$(APP).app" ]; then \
		rm -rf "/Applications/$(APP).app" 2>/dev/null || sudo rm -rf "/Applications/$(APP).app"; \
		echo "removed /Applications/$(APP).app"; \
	fi
	@if [ "$(UNAME_S)" = "Linux" ] && [ -f "$(XDG_APPS)/alpaca.desktop" ]; then \
		rm -f "$(XDG_APPS)/alpaca.desktop"; \
		rm -f "$(XDG_ICONS)"/*/apps/alpaca.png "$(XDG_ICONS)/scalable/apps/alpaca.svg"; \
		echo "removed $(XDG_APPS)/alpaca.desktop"; \
	fi

# ── the desktop app ────────────────────────────────────────────────────────

app: build
	rm -rf $(APPDIR)
	mkdir -p $(APPDIR)/Contents/MacOS $(APPDIR)/Contents/Resources
	install -m 0755 $(BINARY) $(APPDIR)/Contents/MacOS/$(BINARY)
	sed 's/@VERSION@/$(VERSION)/g' packaging/Info.plist > $(APPDIR)/Contents/Info.plist
	-@$(MAKE) --no-print-directory icns
	@if [ -f build/alpaca.icns ]; then cp build/alpaca.icns $(APPDIR)/Contents/Resources/alpaca.icns; fi
	@echo "assembled $(APPDIR)"

app-install: app
	@rm -rf "/Applications/$(APP).app" 2>/dev/null || sudo rm -rf "/Applications/$(APP).app"
	@cp -R $(APPDIR) /Applications/ 2>/dev/null || sudo cp -R $(APPDIR) /Applications/
	@echo "installed /Applications/$(APP).app"

# The Linux launcher: a .desktop entry pointing at the installed binary, and
# the same pixel face as its icon. Both go under $$HOME, so no step here wants
# root and nothing outside the user's own session is touched.
desktop-install: install
	@mkdir -p "$(XDG_APPS)"
	@sed 's|@BINDIR@|$(BINDIR)|g' packaging/alpaca.desktop > "$(XDG_APPS)/alpaca.desktop"
	@chmod 0644 "$(XDG_APPS)/alpaca.desktop"
	@if command -v rsvg-convert >/dev/null 2>&1; then \
		for size in 16 32 48 64 128 256 512; do \
			mkdir -p "$(XDG_ICONS)/$${size}x$${size}/apps"; \
			rsvg-convert -w $$size -h $$size -o "$(XDG_ICONS)/$${size}x$${size}/apps/alpaca.png" assets/icon.svg; \
		done; \
	else \
		mkdir -p "$(XDG_ICONS)/scalable/apps"; \
		cp assets/icon.svg "$(XDG_ICONS)/scalable/apps/alpaca.svg"; \
		echo "rsvg-convert not found (apt install librsvg2-bin) — installed the svg icon instead"; \
	fi
	-@command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "$(XDG_APPS)" 2>/dev/null || true
	-@command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -f -t "$(XDG_ICONS)" 2>/dev/null || true
	@echo "installed $(XDG_APPS)/alpaca.desktop"

# Alias, so `make desktop` reads the way `make app` does on macOS.
desktop: desktop-install

# The icon renders from assets/icon.svg when the tools are around
# (rsvg-convert via homebrew; iconutil ships with macOS). Without them the
# app still works, it just wears the generic icon.
icns:
	@command -v rsvg-convert >/dev/null 2>&1 || { echo "rsvg-convert not found — skipping the icon"; exit 1; }
	@rm -rf build/alpaca.iconset && mkdir -p build/alpaca.iconset
	@for size in 16 32 128 256 512; do \
		rsvg-convert -w $$size -h $$size -o build/alpaca.iconset/icon_$${size}x$${size}.png assets/icon.svg; \
		rsvg-convert -w $$((size * 2)) -h $$((size * 2)) -o build/alpaca.iconset/icon_$${size}x$${size}@2x.png assets/icon.svg; \
	done
	@iconutil -c icns build/alpaca.iconset -o build/alpaca.icns

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

run: build
	./$(BINARY) serve

clean:
	rm -rf $(BINARY) dist/ build/
