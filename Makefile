.PHONY: build test vet check smoke

build:
	go build ./...
	go build -o build/computer-use-jev ./cmd/computer-use-jev

test:
	go test ./...

vet:
	go vet ./...

check: build vet test

# Live smoke test: needs TYPESAFE_API_KEY, macOS Accessibility (and Screen
# Recording for screenshots) granted to the terminal.
smoke:
	go run ./cmd/computer-use-jev -goal "list the running applications"
