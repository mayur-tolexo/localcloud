APP_NAME = localcloud
BINARY = bin/$(APP_NAME)
PORT ?= 8080
DATA_DIR ?= ./data

OS := $(shell uname -s)

build:
	@echo "🧱 Building binary for $(OS)..."
	mkdir -p bin
ifeq ($(OS),Darwin)
	go build -o $(BINARY) ./cmd/server
else
	GOOS=linux GOARCH=arm64 go build -o $(BINARY) ./cmd/server
endif

run-local: build
	@echo "🚀 Running locally on port $(PORT) with data dir $(DATA_DIR)"
	DATA_DIR=$(DATA_DIR) PORT=$(PORT) ./$(BINARY)

docker:
	docker build -t $(APP_NAME) .

clean:
	@echo "🧹 Cleaning..."
	rm -rf bin
	docker rm -f $(APP_NAME) 2>/dev/null || true
