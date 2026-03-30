VERSION ?= $(shell cat VERSION 2>/dev/null || echo v0.1.0)
BINARY  := bridge
MODULE  := agent-wecom-ws-bridge

.PHONY: build run test clean docker release

build:
	CGO_ENABLED=0 go build -o $(BINARY) .

run: build
	./$(BINARY)

test:
	go test ./...

clean:
	rm -f $(BINARY)
	rm -rf dist/

docker:
	docker compose build

up:
	docker compose up -d

down:
	docker compose down

release:
	@mkdir -p dist/release
	@echo "Building $(VERSION) for linux/amd64..."
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/.build/$(BINARY) .
	@mkdir -p dist/.build/$(MODULE)
	@cp dist/.build/$(BINARY) dist/.build/$(MODULE)/
	@cp .env.example dist/.build/$(MODULE)/.env.example
	@tar -czf dist/release/$(MODULE)-$(VERSION)-linux-amd64.tar.gz -C dist/.build $(MODULE)
	@rm -rf dist/.build
	@echo "Built dist/release/$(MODULE)-$(VERSION)-linux-amd64.tar.gz"
