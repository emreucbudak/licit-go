.PHONY: all build run-bidding run-streamer run-payment run-gateway docker-up docker-down tidy

all: build

tidy:
	go mod tidy

build: tidy
	go build -o bin/bidding-engine ./cmd/bidding
	go build -o bin/auction-streamer ./cmd/streamer
	go build -o bin/payment-validator ./cmd/payment
	go build -o bin/gateway ./cmd/gateway

run-bidding:
	go run ./cmd/bidding

run-streamer:
	go run ./cmd/streamer

run-payment:
	go run ./cmd/payment

run-gateway:
	go run ./cmd/gateway

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

test:
	go test ./... -v
