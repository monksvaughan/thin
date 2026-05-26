.PHONY: test build snapshot release-check clean

test:
	go test ./...

build:
	go build -o ./thin ./cmd/thin

snapshot:
	goreleaser release --snapshot --clean

release-check:
	goreleaser check
	goreleaser release --snapshot --clean

clean:
	rm -rf dist thin
