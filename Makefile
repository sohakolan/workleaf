PREFIX ?= /usr/local
ZSH_DIR = $(PREFIX)/share/zsh/site-functions
BASH_DIR = $(PREFIX)/etc/bash_completion.d

build:
	go build -o bin/wl ./cmd/wl

# Also installs tab completion: a file for each shell found, where that shell loads it, and a
# marked block in ~/.zshrc / ~/.bashrc (added once, see `wl completion install`).
install: build
	install -d $(PREFIX)/bin
	install -m 755 bin/wl $(PREFIX)/bin/wl
	if command -v zsh >/dev/null; then \
		install -d $(ZSH_DIR) && bin/wl completion zsh > $(ZSH_DIR)/_wl; fi
	if command -v bash >/dev/null; then \
		install -d $(BASH_DIR) && bin/wl completion bash > $(BASH_DIR)/wl; fi
	if command -v fish >/dev/null; then \
		dir=$$(fish -c 'echo $$__fish_vendor_completionsdirs[1]') && \
		install -d "$$dir" && bin/wl completion fish > "$$dir/wl.fish"; fi
	bin/wl completion install

uninstall: build
	bin/wl completion uninstall
	rm -f $(PREFIX)/bin/wl $(ZSH_DIR)/_wl $(BASH_DIR)/wl
	if command -v fish >/dev/null; then \
		rm -f "$$(fish -c 'echo $$__fish_vendor_completionsdirs[1]')/wl.fish"; fi

.PHONY: build install uninstall
