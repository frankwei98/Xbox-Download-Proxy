.PHONY: test test-race test-cover test-ui test-setup check build vet dist clean

test:
	go test ./...

test-race:
	go test -race -count=1 ./...

test-cover:
	go test -race -count=1 -coverprofile=/tmp/xdp-coverage.out -covermode=atomic ./...
	go tool cover -func=/tmp/xdp-coverage.out

check: test-race test-ui test-setup vet
	node --check internal/app/web/app.js

test-ui:
	node --test scripts/node-selection.test.mjs

test-setup:
	sh scripts/setup_test.sh

build:
	CGO_ENABLED=0 go build -trimpath -o xdp ./cmd/xdp

vet:
	go vet ./...

dist:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/xdp-linux-amd64 ./cmd/xdp
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o dist/xdp-linux-arm64 ./cmd/xdp
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o dist/xdp-darwin-arm64 ./cmd/xdp

clean:
	rm -f xdp
	rm -rf dist
