.PHONY: build test vet validate check-lock all

# Everything CI runs, in the order it runs it.
all: build vet test validate check-lock

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

validate:
	go run ./cmd/meshmedic validate

# The gate that fails when an approved entry was edited, or when one was
# removed without its approval being retired.
check-lock:
	go run ./cmd/meshmedic validate --no-drift
