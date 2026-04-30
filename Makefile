.PHONY: build test lint

build:
	go build -o watchpot .

test:
	go test ./...

lint:
	go vet ./...
