.PHONY: build test vet validate

build:
	go build ./...

test:
	go test ./...

vet:
	go vet ./...

validate:
	go run ./cmd/meshmedic validate
