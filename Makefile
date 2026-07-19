.PHONY: run build test vet fmt-check tidy

run:
	go run ./cmd/robot

build:
	mkdir -p bin
	go build -o bin/robot ./cmd/robot

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

tidy:
	go mod tidy
