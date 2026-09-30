.PHONY: fmt workspace test check sqlite-check verify-install prove-regressions build release-check release-snapshot

fmt:
	go fix ./...
	gofumpt -l -w .

workspace:
	go run tools/dev.go workspace

test:
	go run tools/dev.go check --short

check:
	go run tools/dev.go check

sqlite-check:
	go run tools/dev.go check --sqlite-only

verify-install:
	go run tools/dev.go verify-install

prove-regressions:
	go run tools/dev.go prove-regressions

build: workspace
	go build -o build/xmigrator ./cmd/xmigrator

release-check:
	go run tools/release.go
	go test -race -count=1 tools/release.go tools/release_test.go
	goreleaser check

release-snapshot:
	goreleaser release --snapshot --clean --parallelism 2
