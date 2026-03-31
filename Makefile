.PHONY: all build run-bidding run-streamer run-payment docker-up docker-down tidy

all: build

tidy:
	go mod tidy

build: tidy
	go build -o bin/bidding-engine ./cmd/bidding
	go build -o bin/auction-streamer ./cmd/streamer
	go build -o bin/payment-validator ./cmd/payment

run-bidding:
	go run ./cmd/bidding

run-streamer:
	go run ./cmd/streamer

run-payment:
	go run ./cmd/payment

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down

test:
	go test ./... -v
