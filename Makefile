VENV := $(HOME)/.local/share/vigil/venv
PYTHON := $(VENV)/bin/python
VERSION := $(shell $(PYTHON) -c "import tomllib; print(tomllib.loads(open('pyproject.toml').read())['project']['version'])" 2>/dev/null || python3 -c "import tomllib; print(tomllib.loads(open('pyproject.toml').read())['project']['version'])")
TAP_REPO := joshuazd/homebrew-tap

.PHONY: install test lint clean release

install:
	./vigil --help > /dev/null

test:
	$(PYTHON) -m pytest tests/ -v

lint:
	$(PYTHON) -m ruff check src/ tests/

clean:
	rm -rf $(VENV)

release: test lint
	@echo "Releasing v$(VERSION)..."
	@if git rev-parse "v$(VERSION)" >/dev/null 2>&1; then \
		echo "Error: tag v$(VERSION) already exists. Bump version in pyproject.toml first."; \
		exit 1; \
	fi
	git tag "v$(VERSION)"
	git push origin "v$(VERSION)"
	gh release create "v$(VERSION)" --title "v$(VERSION)" --generate-notes
	@echo ""
	@echo "Waiting for PyPI publish..."
	@for i in 1 2 3 4 5 6 7 8 9 10 11 12; do \
		url="https://files.pythonhosted.org/packages/source/v/vigil-tui/vigil_tui-$(VERSION).tar.gz"; \
		if curl -sfI "$$url" > /dev/null 2>&1; then \
			echo "PyPI tarball available."; \
			break; \
		fi; \
		if [ $$i -eq 12 ]; then \
			echo "Timed out waiting for PyPI. Update Homebrew formula manually."; \
			exit 0; \
		fi; \
		echo "  Not yet available, retrying in 30s... ($$i/12)"; \
		sleep 30; \
	done
	@echo "Updating Homebrew formula..."
	$(MAKE) bump-tap

bump-tap:
	@url="https://files.pythonhosted.org/packages/source/v/vigil-tui/vigil_tui-$(VERSION).tar.gz"; \
	sha=$$(curl -sfL "$$url" | shasum -a 256 | cut -d' ' -f1); \
	if [ -z "$$sha" ]; then \
		echo "Error: could not fetch tarball from PyPI"; \
		exit 1; \
	fi; \
	echo "SHA256: $$sha"; \
	tmpdir=$$(mktemp -d); \
	gh repo clone $(TAP_REPO) "$$tmpdir" -- -q; \
	cd "$$tmpdir" && \
	sed -i '' 's|url "https://files.pythonhosted.org/packages/source/v/vigil-tui/vigil_tui-.*\.tar\.gz"|url "'"$$url"'"|' Formula/vigil.rb && \
	sed -i '' '0,/sha256 "[a-f0-9]*"/s|sha256 "[a-f0-9]*"|sha256 "'"$$sha"'"|' Formula/vigil.rb && \
	git add Formula/vigil.rb && \
	git commit -m "vigil $(VERSION)" && \
	git push && \
	echo "Homebrew formula updated to $(VERSION)"; \
	rm -rf "$$tmpdir"
