.PHONY: dev build test check
dev:
	go run . serve
build:
	go build -trimpath -o bin/fennec .
test:
	go test -race ./...
check:
	go vet ./...
	test -z "$$(gofmt -l .)"
