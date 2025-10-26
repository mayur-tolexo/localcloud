# Build stage (ARM64 compatible)
FROM golang:1.21-bullseye AS build

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Build for linux/arm64 (Pi 5)
RUN CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -o /localcloud ./cmd/server

# Runtime
FROM debian:bullseye-slim
RUN apt-get update && apt-get install -y ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY --from=build /localcloud /app/localcloud

ENV DATA_DIR=/data
EXPOSE 8080

VOLUME ["/data"]

CMD ["./localcloud"]
