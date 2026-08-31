BIN := sand
GO ?= go

.PHONY: build test lint mac check install redeploy ship sync skills clean

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

# Mac from box, run on the Mac, in the Mac's copy. The usual direction.
sync:
	@[ -n "$(BOX)" ] || { echo "usage: make sync BOX=<sandbox-ssh-alias>   (run on the Mac)"; exit 1; }
	rsync -a --delete --exclude build $(BOX):projects/sand/ ./
	$(MAKE) install

# One skill, both harnesses on the box read it. Run on the box, not the Mac.
skills:
	@for d in $(HOME)/.claude/skills $(HOME)/.pi/agent/skills; do \
		mkdir -p $$d && ln -sfn $(CURDIR)/skills/sand-comments $$d/sand-comments && echo "$$d/sand-comments"; \
	done

clean:
	rm -rf build
