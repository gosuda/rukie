.PHONY: fmt vet lint test vuln check goals build-web web-assets

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
	tools/passgate.sh '--- PASS: ' -- go test -race -count=1 -v -timeout 30m ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

check: web-assets
	@rc=0; \
	$(MAKE) -k vet lint test vuln || rc=$$?; \
	test -z "$$(git ls-files '*.go' | xargs -r gofmt -l)" || rc=$$?; \
	go run ./tools/commentcheck || rc=$$?; \
	go mod tidy -diff || rc=$$?; \
	go mod verify || rc=$$?; \
	exit $$rc

web-assets:
	@if [ -d web/node_modules ]; then \
		$(MAKE) build-web; \
	else \
		test -s internal/web/assets/app.js || { echo "web bundle missing and web/node_modules absent: run make build-web"; exit 1; }; \
	fi

goals:
	go test -count=1 -v ./.agent-tasks/task-0e2cc1171807/tests
