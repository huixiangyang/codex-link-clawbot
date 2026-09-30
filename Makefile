.PHONY: dev fmt docs-check format-check vet test test-fast build check check-fast fuzz-smoke

# macOS 的默认临时目录包含 /var 符号链接且路径过长；使用真实短路径保留状态安全检查。
export TMPDIR := $(shell cd /tmp && pwd -P)
PACKAGES ?= ./...

dev:
	air -c .air.toml

fmt:
	@find . -type f -name '*.go' -not -path './.git/*' -not -path './tmp/*' -print0 | xargs -0 gofmt -w

docs-check:
	./scripts/check-doc-links.sh

format-check:
	@unformatted="$$(find . -type f -name '*.go' -not -path './.git/*' -not -path './tmp/*' -print0 | xargs -0 gofmt -l)"; \
		test -z "$$unformatted" || (printf '%s\n' "$$unformatted" && exit 1)

vet:
	go vet ./...

test:
	go test $(PACKAGES) -count=1 -race

test-fast:
	go test $(PACKAGES) -count=1

build:
	go build ./cmd/codex-link-clawbot

check: docs-check format-check vet test build

check-fast: docs-check format-check vet test-fast build

fuzz-smoke:
	go test ./internal/config -run='^$$' -fuzz='^FuzzDecodeConfig$$' -fuzztime=5s
	go test ./internal/ilink -run='^$$' -fuzz='^FuzzDecodeGetUpdatesResponse$$' -fuzztime=5s
	go test ./internal/bridge -run='^$$' -fuzz='^FuzzValidateInboundFile$$' -fuzztime=5s
	go test ./internal/bridge -run='^$$' -fuzz='^FuzzValidatedImageExtension$$' -fuzztime=5s
	go test ./internal/codex/appserver -run='^$$' -fuzz='^FuzzCodexEventDecoders$$' -fuzztime=5s
