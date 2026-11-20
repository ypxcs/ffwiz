GO ?= go
BIN := bin/ffwiz
PKG := ./...

.PHONY: all build test test-integration lint fmt vet run dump samples clean

all: lint test build

build:
	$(GO) build -o $(BIN) ./cmd/ffwiz

## test: unit + model tests. Integration tests are skipped unless ffmpeg is present.
test:
	$(GO) test $(PKG)

## test-integration: run the real end-to-end tests that shell out to ffmpeg.
test-integration:
	FFWIZ_INTEGRATION=1 $(GO) test -run Integration $(PKG)

lint: fmt vet

fmt:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet $(PKG)

run:
	$(GO) run ./cmd/ffwiz

## dump: print the command a task would generate without launching the TUI.
##   make dump TASK=convert_video ARGS="--answer container=mp4 --answer vcodec=libx264"
dump:
	$(GO) run ./cmd/ffwiz --dump $(TASK) $(ARGS)

## samples: (re)generate the tiny media files used by tests and manual checks.
samples:
	./scripts/make-samples.sh

clean:
	rm -rf bin
