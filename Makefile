.PHONY: check

check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	go test ./...
	go run ./cmd/owl policy lint
	cd perception && uv run python -m unittest discover -s tests
