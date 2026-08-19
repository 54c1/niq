PREFIX ?= $(HOME)/.local/bin

.PHONY: install build

build:
	go build -ldflags "-X main.version=$(shell git describe --tags --always 2>/dev/null || echo dev)" -o ./bin/niq ./cmd/niq/

# Full local test of the release pipeline (no upload, no publish).
snapshot:
	goreleaser release --snapshot --clean
	./scripts/publish-npm.sh "$(shell git describe --tags --always 2>/dev/null || echo 0.0.0-dev)" ./dist --dry-run

install: build
	mkdir -p $(PREFIX)
	ln -sf "$(shell pwd)/bin/niq" $(PREFIX)/niq
	@echo "niq installed → $(PREFIX)/niq"
	@echo "Make sure $(PREFIX) is in your PATH"
