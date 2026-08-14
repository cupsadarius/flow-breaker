BINARY := flow-breaker
# ssh host alias; override: make deploy PI_HOST=user@host
PI_HOST ?= pi

.PHONY: build build-arm64 deploy test

build:
	go build -o $(BINARY) .

build-arm64:
	GOOS=linux GOARCH=arm64 go build -o $(BINARY) .

deploy: build-arm64
	scp $(BINARY) $(PI_HOST):~/.local/bin/$(BINARY)
	ssh $(PI_HOST) 'systemctl --user restart flow-breakerd'

test:
	go test ./... -race
