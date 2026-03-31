GO := go
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build install test lint clean release

build:
	$(GO) build -ldflags "-X main.version=$(VERSION)" -o vigil .

install: build
	\cp vigil ~/.local/bin/vigil

test:
	$(GO) test ./...

lint:
	golangci-lint run

clean:
	rm -f vigil

release: test
	@if git rev-parse "v$(VERSION)" >/dev/null 2>&1; then \
		echo "Error: tag v$(VERSION) already exists."; \
		exit 1; \
	fi
	git tag "v$(VERSION)"
	git push origin "v$(VERSION)"
	gh release create "v$(VERSION)" --title "v$(VERSION)" --generate-notes
