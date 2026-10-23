# streamr build & dev tasks. Works on Linux and FreeBSD (GNU make or bmake).

BIN      := bin/streamr
PKG      := ./cmd/streamr
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -s -w -X main.version=$(VERSION)

.PHONY: all build web run dev test vet fmt clean dist \
        build-linux build-freebsd build-freebsd-arm64

all: web build

## build: compile the server for the host OS
build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) $(PKG)

## web: install deps and build the React frontend into web/dist
web:
	cd web && npm install && npm run build

## run: build and run with ./config.yaml
run: build
	./$(BIN) -config config.yaml

## dev: run the Go server (expects a prebuilt web/dist or web_dir unset)
dev:
	go run $(PKG) -config config.yaml

## test / vet / fmt
test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

## clean: remove build artifacts
clean:
	rm -rf bin dist

## dist: cross-compile release binaries for Linux + FreeBSD
dist: build-linux build-freebsd build-freebsd-arm64

build-linux:
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/streamr-linux-amd64   $(PKG)

build-freebsd:
	CGO_ENABLED=0 GOOS=freebsd GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/streamr-freebsd-amd64 $(PKG)

build-freebsd-arm64:
	CGO_ENABLED=0 GOOS=freebsd GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/streamr-freebsd-arm64 $(PKG)
