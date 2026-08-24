.PHONY: build run clean

BINARY := atlas-runtime
LDFLAGS := -ldflags="-s -w"

build:
	@echo "Building $(BINARY) for linux/amd64 ..."
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)-linux-amd64 .
	@echo "Building $(BINARY) for linux/arm64 ..."
	GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY)-linux-arm64 .
	@echo "Done."

run:
	scripts/atlas-dev.sh $(VM)

clean:
	rm -rf dist/
