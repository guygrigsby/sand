BIN := sand
GO ?= go

# The box for sync comes from sand's own config, so it is named in exactly one place and
# `sand config set host <alias>` moves both the tool and this Makefile. This checkout first:
# `make sync` runs before the install that would refresh the installed binary, and an older
# one without `config get` answers with the whole config file. But the checkout is also what
# sync replaces, so it can be mid-edit and not compile, which is one of the reasons to sync
# in the first place; then the installed sand answers instead, and the one-word check below
# still catches one too old to know `config get`. Neither one: `BOX=<alias>`.
# Expanded once, inside the sync recipe, so no other target pays for the compile.
BOX ?= $(shell $(GO) run . config get host 2>/dev/null || sand config get host 2>/dev/null)

.PHONY: build test lint mac check install redeploy ship sync clean

build:
	$(GO) build -o build/$(BIN) .

test:
	$(GO) test ./...

lint:
	$(GO) vet ./...
	@out=$$(gofmt -l .); [ -z "$$out" ] || { echo "gofmt wants:"; echo "$$out"; exit 1; }

# The Mac is the real target, so a build for it is part of the gate.
mac:
	GOOS=darwin GOARCH=arm64 $(GO) build -o build/$(BIN)-darwin-arm64 .

check: lint test build mac

# GOBIN, or ~/go/bin.
install:
	$(GO) install .

redeploy: install

# Box to Mac, run here, when the Mac accepts ssh from the sandbox.
ship:
	@[ -n "$(MAC)" ] || { echo "usage: make ship MAC=user@mac"; exit 1; }
	rsync -a --delete --exclude build --exclude .git ./ $(MAC):src/sand/
	ssh $(MAC) 'make -C src/sand install'

# Mac from box, run on the Mac, in the Mac's copy. The usual direction, so it takes no
# arguments; BOX only needs naming for a different box.
#
# Excludes .git, like ship does. Source is what travels; git state is each machine's own, and
# the Mac's is not reproducible from here: its remotes (origin on GitHub, and the one pointing
# back at this box), its hooks, its HEAD, its reflog. Syncing over it deleted an origin
# `gh repo create` had just made, twice.
#
# So the commits come by fetch instead, from whichever remote points back at the box, which is
# what the Mac then signs, pushes and merges. Failure there is not fatal (that remote may not
# exist yet, and the network may be down), and nothing is merged or checked out: which branch,
# and whether it is signed yet, is the Mac's call. `git switch -C <branch> <box-remote>/<branch>`
# is what makes the rsynced files stop reading as uncommitted changes.
sync:
	@box="$(BOX)"; \
	case "x$$box" in x) echo "no box: pass BOX=<alias> or run \`sand config init\`"; exit 1;; \
	  *[[:space:]]*) echo "BOX is not one host: $$box"; exit 1;; esac; \
	set -x; rsync -a --delete --exclude build --exclude .git "$$box":projects/sand/ ./
	-git fetch --all
	$(MAKE) install

clean:
	rm -rf build
