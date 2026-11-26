 BINARY_NAME=xconnect-edge-agent
INSTALL_BIN=/usr/local/bin/$(BINARY_NAME)
SERVICE_NAME=xconnect-edge-agent

.PHONY: all build install upgrade cf-worker-install cf-worker-dev cf-worker-check cf-worker-deploy cf-containers-install cf-containers-dev cf-containers-check cf-containers-deploy

all: build

export PATH := $(PATH):/usr/local/go/bin

GO_BUILD_TAGS ?=

build:
	/usr/local/go/bin/go mod tidy
	/usr/local/go/bin/go build -tags "$(GO_BUILD_TAGS)" -o $(BINARY_NAME) ./cmd/agent

install: build
	install -m 755 $(BINARY_NAME) $(INSTALL_BIN)
	@echo "Installed $(BINARY_NAME) to $(INSTALL_BIN)"

upgrade: install
	systemctl restart $(SERVICE_NAME)
	@echo "Restarted $(SERVICE_NAME)"

cf-worker-install:
	cd deploy/cloudflare/workers && npm install

cf-worker-dev:
	cd deploy/cloudflare/workers && npm run dev

cf-worker-check:
	cd deploy/cloudflare/workers && npm run check

cf-worker-deploy:
	cd deploy/cloudflare/workers && npm run deploy

cf-containers-install:
	cd deploy/cloudflare/containers && npm install

cf-containers-dev:
	cd deploy/cloudflare/containers && npm run dev

cf-containers-check:
	cd deploy/cloudflare/containers && npm run check

cf-containers-deploy:
	cd deploy/cloudflare/containers && npm run deploy
