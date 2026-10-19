.PHONY: test test-ui test-setup build vet dist clean

test:
	go test ./...

test-ui:
	node --test scripts/node-selection.test.mjs

test-setup:
	bash scripts/setup_test.sh

build:
	CGO_ENABLED=0 go build -trimpath -o xdp ./cmd/xdp

vet:
	go vet ./...

dist:
	mkdir -p dist
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/xdp-linux-amd64 ./cmd/xdp
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o dist/xdp-linux-arm64 ./cmd/xdp
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o dist/xdp-darwin-amd64 ./cmd/xdp
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o dist/xdp-darwin-arm64 ./cmd/xdp

clean:
	rm -f xdp
	rm -rf dist
