BINARY := flow-breaker
# ssh host alias; override: make deploy PI_HOST=user@host
PI_HOST ?= pi

.PHONY: build build-arm64 deploy test

build:
	go build -o $(BINARY) .

build-arm64:
	GOOS=linux GOARCH=arm64 go build -o $(BINARY) .

deploy: build-arm64
	# scp to a temp name then atomic mv — can't truncate the running binary (ETXTBSY).
	# XDG_RUNTIME_DIR so `systemctl --user` works over a non-login ssh.
	scp $(BINARY) $(PI_HOST):~/.local/bin/$(BINARY).new
	ssh $(PI_HOST) 'export XDG_RUNTIME_DIR=/run/user/$$(id -u); \
		mv ~/.local/bin/$(BINARY).new ~/.local/bin/$(BINARY) && \
		systemctl --user restart flow-breakerd'

test:
	go test ./... -race
