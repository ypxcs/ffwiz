GO ?= go
BIN := bin/ffwiz
PKG := ./...

.PHONY: all build test test-integration lint fmt vet run dump samples release-check clean

all: lint test build

build:
	$(GO) build -o $(BIN) ./cmd/ffwiz

## test: unit and model tests. The integration tests skip themselves when ffmpeg
## or the generated sample media are missing.
test:
	$(GO) test $(PKG)

## test-integration: just the tests that shell out to real ffmpeg.
test-integration:
	$(GO) test -v -count=1 -run Integration $(PKG)

## lint: gofmt and vet always; golangci-lint too when it is installed, since it
## carries the project's configuration (.golangci.yml) and CI runs it.
lint: fmt vet
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run $(PKG); \
	else \
		echo "golangci-lint not installed; skipping it (CI runs it)"; \
	fi

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet $(PKG)

run:
	$(GO) run ./cmd/ffwiz

## dump: print the command a task would generate, without launching the TUI.
##   make dump TASK=convert_video INPUT=clip.mp4 ARGS="--answers container=webm"
dump:
	@test -n "$(TASK)" || { echo 'usage: make dump TASK=<task-id> INPUT=<file> [ARGS="--answers step=value"]'; exit 2; }
	@test -n "$(INPUT)" || { echo 'usage: make dump TASK=<task-id> INPUT=<file> [ARGS="--answers step=value"]'; exit 2; }
	$(GO) run ./cmd/ffwiz --dump $(TASK) --input "$(INPUT)" $(ARGS)

## samples: (re)generate the tiny media files used by tests and manual checks.
samples:
	./scripts/make-samples.sh

## release-check: build every release target without publishing anything
## (plan.md §9). `goreleaser check` is left to CI: it resolves the git remote to
## validate the release section, and a local clone may have no remote yet.
release-check:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin dist
