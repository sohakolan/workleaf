PREFIX ?= /usr/local

build:
	go build -o bin/wl ./cmd/wl

install: build
	install -d $(PREFIX)/bin
	install -m 755 bin/wl $(PREFIX)/bin/wl

.PHONY: build install
