.PHONY: build test vet check smoke

build:
	go build ./...
	go build -o build/computer_use ./cmd/computeruser

test:
	go test ./...

vet:
	go vet ./...

check: build vet test

# Live smoke test: needs TYPESAFE_API_KEY, macOS Accessibility (and Screen
# Recording for screenshots) granted to the terminal.
smoke:
	go run ./cmd/computeruser -goal "list the running applications"
