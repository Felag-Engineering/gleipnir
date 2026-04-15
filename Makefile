.PHONY: help build test security security-go security-frontend

help:
	@echo "Targets:"
	@echo "  build              go build ./..."
	@echo "  test               go test -race ./..."
	@echo "  security           run all security scans (govulncheck + npm audit)"
	@echo "  security-go        govulncheck ./..."
	@echo "  security-frontend  npm audit --omit=dev (in frontend/)"

build:
	go build ./...

test:
	go test -race ./...

security: security-go security-frontend

security-go:
	govulncheck ./...

security-frontend:
	cd frontend && npm audit --omit=dev
