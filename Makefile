BINARY := flow-breaker
# ssh host alias; override: make deploy PI_HOST=user@host
PI_HOST ?= pi@rpi

.PHONY: build build-arm64 deploy test

build:
	go build -o $(BINARY) .

build-arm64:
	# -s -w strips symbols/DWARF: ~30% smaller binary → faster scp.
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o $(BINARY) .

deploy: build-arm64
	# scp to a temp name then atomic mv — can't truncate the running binary (ETXTBSY).
	# XDG_RUNTIME_DIR so `systemctl --user` works over a non-login ssh.
	# -O forces the legacy SCP protocol (new SFTP mode stalls on some sshd configs).
	# -C compresses in transit.
	scp -O -C $(BINARY) $(PI_HOST):~/.local/bin/$(BINARY).new
	ssh $(PI_HOST) 'export XDG_RUNTIME_DIR=/run/user/$$(id -u); \
		mv ~/.local/bin/$(BINARY).new ~/.local/bin/$(BINARY) && \
		systemctl --user restart flow-breakerd'

test:
	go test ./... -race
