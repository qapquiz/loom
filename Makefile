# Reproducible native build. libghostty-vt is fetched and built from a
# pinned ghostty commit into ./build; no sibling checkout or untracked
# build dir elsewhere is consulted.
#
# The zig flags mirror what ghostty's own CMakeLists.txt runs for a
# release build: -Demit-lib-vt -Demit-xcframework=false
# -Doptimize=ReleaseFast (CMake RELEASE maps to ReleaseFast).

GHOSTTY_REPO ?= https://github.com/ghostty-org/ghostty.git
GHOSTTY_SHA := 5813c9461d5af78b6307a1254d00b1bc190eaccd

BUILD_DIR := build
GHOSTTY_SRC := $(BUILD_DIR)/ghostty
LIBGHOSTTY_PREFIX := $(BUILD_DIR)/libghostty
PKG_CONFIG_PATH := $(CURDIR)/$(LIBGHOSTTY_PREFIX)/lib/pkgconfig
LD_LIBRARY_PATH := $(CURDIR)/$(LIBGHOSTTY_PREFIX)/lib

# Stamp keyed on the pinned commit: re-running make deps is a no-op,
# changing GHOSTTY_SHA re-fetches and rebuilds.
DEPS_STAMP := $(BUILD_DIR)/.deps-$(GHOSTTY_SHA)

.PHONY: deps build test run clean

deps: $(DEPS_STAMP)

$(DEPS_STAMP):
	@mkdir -p $(BUILD_DIR)
	@if [ ! -d $(GHOSTTY_SRC)/.git ]; then \
		git clone --depth 1 $(GHOSTTY_REPO) $(GHOSTTY_SRC); \
	fi
	git -C $(GHOSTTY_SRC) fetch --depth 1 origin $(GHOSTTY_SHA)
	git -C $(GHOSTTY_SRC) checkout --detach $(GHOSTTY_SHA)
	cd $(GHOSTTY_SRC) && zig build -Demit-lib-vt -Demit-xcframework=false \
		-Doptimize=ReleaseFast --prefix $(CURDIR)/$(LIBGHOSTTY_PREFIX)
	PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) pkg-config --modversion libghostty-vt
	@touch $@

build: $(DEPS_STAMP)
	PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) go build ./...

test: $(DEPS_STAMP)
	PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) LD_LIBRARY_PATH=$(LD_LIBRARY_PATH) \
		go test -race ./...

run: $(DEPS_STAMP)
	PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) LD_LIBRARY_PATH=$(LD_LIBRARY_PATH) \
		go run ./cmd/loom

clean:
	rm -rf $(BUILD_DIR)
