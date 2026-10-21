.PHONY: fmt vet lint test vuln check goals build-web

build-web:
	cd web && pnpm install --frozen-lockfile
	cd web && pnpm run build

fmt:
	gofmt -w .

vet:
	go vet ./...
	go vet -tags integration,e2e,perf,render ./...

lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./...

test:
	tools/passgate.sh '--- PASS: ' -- go test -race -count=1 -v ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

check: vet lint test vuln
	test -z "$$(git ls-files '*.go' | xargs -r gofmt -l)"
	go run ./tools/commentcheck
	go mod tidy -diff
	go mod verify

goals:
	go test -count=1 -v ./.agent-tasks/task-0e2cc1171807/tests
