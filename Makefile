GO ?= go

.PHONY: build windows test

build:
	mkdir -p bin
	$(GO) build -o bin/pvbrowser ./cmd/pvbrowser
	$(GO) build -o bin/pvcli .

windows:
	mkdir -p bin
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -o bin/pvbrowser.exe ./cmd/pvbrowser
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 $(GO) build -o bin/pvcli.exe .

test:
	$(GO) test ./...
