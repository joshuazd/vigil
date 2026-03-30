VENV := $(HOME)/.local/share/vigil/venv
PYTHON := $(VENV)/bin/python
VERSION := $(shell $(PYTHON) -c "import tomllib; print(tomllib.loads(open('pyproject.toml').read())['project']['version'])" 2>/dev/null || python3 -c "import tomllib; print(tomllib.loads(open('pyproject.toml').read())['project']['version'])")
.PHONY: install test lint clean release

install:
	./vigil --help > /dev/null

test:
	$(PYTHON) -m pytest tests/ -v

lint:
	$(PYTHON) -m ruff check src/ tests/
	$(PYTHON) -m mypy src/vigil/ --ignore-missing-imports

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
