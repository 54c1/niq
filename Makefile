PREFIX ?= $(HOME)/.local/bin

.PHONY: install build

build:
	go build -o ./bin/niq ./cmd/niq/

install: build
	mkdir -p $(PREFIX)
	ln -sf "$(shell pwd)/bin/niq" $(PREFIX)/niq
	@echo "niq installed → $(PREFIX)/niq"
	@echo "Make sure $(PREFIX) is in your PATH"
