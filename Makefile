VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST := dist

.PHONY: build build-all build-amd64 build-arm64 build-armv7 run clean

build:
	go build -ldflags="$(LDFLAGS)" -o $(DIST)/downbox .

build-all: build-amd64 build-arm64 build-armv7

build-amd64:
	GOOS=linux GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o $(DIST)/downbox-amd64 .

build-arm64:
	GOOS=linux GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o $(DIST)/downbox-arm64 .

build-armv7:
	GOOS=linux GOARCH=arm GOARM=7 go build -ldflags="$(LDFLAGS)" -o $(DIST)/downbox-armv7 .

run:
	go run . --dev

run-build: build
	./$(DIST)/downbox

install: build
	cp $(DIST)/downbox /usr/local/bin/downbox
	@echo "Installed. Run: downbox"

clean:
	rm -rf $(DIST)
