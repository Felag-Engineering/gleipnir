.PHONY: help build test security security-go security-frontend proto

help:
	@echo "Targets:"
	@echo "  build              go build ./..."
	@echo "  test               go test -race ./..."
	@echo "  security           run all security scans (govulncheck + npm audit)"
	@echo "  security-go        govulncheck ./..."
	@echo "  security-frontend  npm audit --omit=dev (in frontend/)"
	@echo "  proto              regenerate plugin-sdk/gen/ from .proto sources (requires buf)"

build:
	go build ./...

test:
	go test -race ./...

security: security-go security-frontend

security-go:
	govulncheck ./...

security-frontend:
	cd frontend && npm audit --omit=dev

# Regenerate gRPC/protobuf stubs from .proto sources.
# Requires buf: https://buf.build/docs/installation
# Pinned plugin versions are declared in buf.gen.yaml.
# The proto-gen-drift CI job ensures checked-in stubs are never stale.
proto:
	buf generate
