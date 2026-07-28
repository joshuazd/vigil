GO := go
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build install test lint clean release release-dry

build:
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o vigil .

install: build
	\cp vigil ~/.local/bin/vigil

# -race is not optional here: the daemon's entire design is a concurrency
# claim, and TestRunWaitsForWriters only fails reliably under the detector.
test:
	$(GO) test -race ./...

lint:
	golangci-lint run

clean:
	rm -f vigil

LATEST_TAG := $(shell git describe --tags --abbrev=0 2>/dev/null || echo v0.0.0)
MAJOR := $(word 1,$(subst ., ,$(LATEST_TAG:v%=%)))
MINOR := $(word 2,$(subst ., ,$(LATEST_TAG:v%=%)))
PATCH := $(word 3,$(subst ., ,$(LATEST_TAG:v%=%)))

release-patch: v = $(MAJOR).$(MINOR).$(shell echo $$(($(PATCH)+1)))
release-minor: v = $(MAJOR).$(shell echo $$(($(MINOR)+1))).0
release-major: v = $(shell echo $$(($(MAJOR)+1))).0.0

release-dry:
	goreleaser release --snapshot --clean

release release-patch release-minor release-major: lint test
	@if [ -z "$(v)" ]; then echo "Usage: make release v=1.0.1"; exit 1; fi
	@if [ -n "$$(git status --porcelain)" ]; then echo "Error: working tree is dirty — commit or stash first."; exit 1; fi
	@if git rev-parse "v$(v)" >/dev/null 2>&1; then \
		echo "Error: tag v$(v) already exists."; \
		exit 1; \
	fi
	git tag "v$(v)"
	git push origin "v$(v)"
	gh release create "v$(v)" --title "v$(v)" --generate-notes
