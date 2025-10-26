# Go Backend Makefile

APP_NAME=localcloud
BINARY=bin/$(APP_NAME)
DOCKER_IMAGE=localcloud:latest
GO_FILES=$(shell find . -name '*.go')

# Configurable vars
DATA_DIR?=/data
QDRANT_URL?=http://qdrant:6333
AI_SERVICE_URL?=http://ai-service:5000
PI_IP?=127.0.0.1

.PHONY: all build run clean docker docker-run docker-push

all: build

## 🔧 Build the Go binary
build:
	@echo "👉 Building $(APP_NAME)..."
	mkdir -p bin
	GOOS=linux GOARCH=arm64 go build -o $(BINARY) ./cmd/server/main.go
	@echo "✅ Build complete: $(BINARY)"

## 🧪 Run the app locally (outside Docker)
run:
	@echo "🚀 Running $(APP_NAME)..."
	PI_IP=$(PI_IP) DATA_DIR=$(DATA_DIR) QDRANT_URL=$(QDRANT_URL) AI_SERVICE_URL=$(AI_SERVICE_URL) \
	go run ./cmd/server/main.go

## 🧹 Clean build files
clean:
	@echo "🧹 Cleaning..."
	rm -rf bin

## 🐳 Build Docker image
docker:
	@echo "🐳 Building Docker image..."
	docker build -t $(DOCKER_IMAGE) .

## ▶️ Run Docker container (standalone test)
docker-run:
	@echo "▶️ Running container..."
	docker run -d \
		--name $(APP_NAME) \
		--restart always \
		-p 8080:8080 \
		-e DATA_DIR=$(DATA_DIR) \
		-e QDRANT_URL=$(QDRANT_URL) \
		-e AI_SERVICE_URL=$(AI_SERVICE_URL) \
		-e PI_IP=$(PI_IP) \
		-v $(DATA_DIR):/data \
		$(DOCKER_IMAGE)

## 🛑 Stop Docker container
stop:
	@echo "🛑 Stopping container..."
	docker stop $(APP_NAME) || true
	docker rm $(APP_NAME) || true

## 🚀 Full rebuild (clean + docker build + run)
rebuild: clean docker stop docker docker-run

## 🔍 Logs from running container
logs:
	docker logs -f $(APP_NAME)
