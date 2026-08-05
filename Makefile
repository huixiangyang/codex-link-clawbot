.PHONY: dev format-check vet test build check fuzz-smoke

dev:
	air -c .air.toml start

format-check:
	@unformatted="$$(find . -type f -name '*.go' -not -path './.git/*' -print0 | xargs -0 gofmt -l)"; \
		test -z "$$unformatted" || (printf '%s\n' "$$unformatted" && exit 1)

vet:
	go vet ./...

test:
	go test ./... -count=1 -race

build:
	go build ./...

check: format-check vet test build

fuzz-smoke:
	go test ./config -run='^$$' -fuzz='^FuzzDecodeConfig$$' -fuzztime=5s
	go test ./ilink -run='^$$' -fuzz='^FuzzDecodeGetUpdatesResponse$$' -fuzztime=5s
	go test ./messaging -run='^$$' -fuzz='^FuzzValidateInboundFile$$' -fuzztime=5s
	go test ./messaging -run='^$$' -fuzz='^FuzzValidatedImageExtension$$' -fuzztime=5s
	go test ./codex -run='^$$' -fuzz='^FuzzCodexEventDecoders$$' -fuzztime=5s
