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
# A fetch, not an rsync. The Mac's copy is a git checkout, and rsync fought it from both ends:
# with .git included it deleted the Mac's own remotes and hooks, and with .git excluded it laid
# the box's working tree over a different commit, so every file the box had committed since read
# as an uncommitted Mac-side change, and nothing there could be checked out or signed until they
# were thrown away. Committed history is the only thing the Mac has any use for anyway: it signs,
# pushes and merges, and there is nothing to sign in an uncommitted file.
#
# Fetched by URL rather than through a named remote, since BOX already names the box and a remote
# is one more thing to keep pointing at the same place. --ff-only because a Mac-side branch that
# has moved is signed history, and the answer to that is `sand sign`, which pushes it to the box,
# not a silently discarded rewrite. A dirty tree stops it too, which is the point.
#
# A failed fast forward gets named rather than passed through: git's hint is written for a repo
# where merging is an option, and here it never is. Two cases, and they want opposite answers, so
# guessing between them is how the wrong one gets run.
sync:
	@box="$(BOX)"; \
	case "x$$box" in x) echo "no box: pass BOX=<alias> or run \`sand config init\`"; exit 1;; \
	  *[[:space:]]*) echo "BOX is not one host: $$box"; exit 1;; esac; \
	branch=$$(git symbolic-ref --quiet --short HEAD) || \
	  { echo "detached HEAD: switch to the branch you want from the box"; exit 1; }; \
	git diff --quiet HEAD || \
	  { echo "uncommitted changes here. The box is where code is edited, so this Mac has"; \
	    echo "nothing to keep: commit them on the box, or \`git checkout .\` and re-sync."; exit 1; }; \
	set -x; git fetch "$$box:projects/sand" "$$branch" || exit 1; \
	{ set +x; } 2>/dev/null; \
	if git merge --ff-only FETCH_HEAD; then :; \
	elif git merge-base --is-ancestor HEAD FETCH_HEAD; then \
	  echo "the history fast forwards, the checkout does not: git's error above is the one to"; \
	  echo "read. Usually an untracked file here where the box has a committed one."; exit 1; \
	else \
	  echo "$$branch has moved on both machines, so no fast forward can say which is right."; \
	  echo "The Mac must not merge, the box does: \`git pull\` there to settle it, then re-sync."; \
	  echo "If this Mac's commits are only the signing, \`git reset --hard FETCH_HEAD\` drops"; \
	  echo "them and \`sand sign\` re-does them."; exit 1; \
	fi
	$(MAKE) install

clean:
	rm -rf build
