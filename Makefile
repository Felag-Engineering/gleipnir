.PHONY: help build test security security-go security-frontend proto lint-plugins lint-plugins-self-test

help:
	@echo "Targets:"
	@echo "  build                      go build ./..."
	@echo "  test                       go test -race ./..."
	@echo "  security                   run all security scans (govulncheck + npm audit)"
	@echo "  security-go                govulncheck ./..."
	@echo "  security-frontend          npm audit --omit=dev (in frontend/)"
	@echo "  proto                      regenerate plugin-sdk/gen/ from .proto sources (requires buf)"
	@echo "  lint-plugins               enforce /plugins/* import boundary"
	@echo "  lint-plugins-self-test     prove the lint catches a deliberate violation"

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

lint-plugins:
	./scripts/lint-plugins.sh

lint-plugins-self-test:
	./scripts/lint-plugins-self-test.sh
