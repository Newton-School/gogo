SHELL := /bin/bash

.DEFAULT_GOAL := help

# AgentFlow architecture review
-include .agentflow/agentflow.mk

.PHONY: help test test-js test-modules test-integration audit-dependencies vet build
help: agentflow-help
	@echo 'Product: make test | test-js | test-modules | test-integration | audit-dependencies | vet | build'
	@echo 'Documentation: make docs-dev | docs | docs-serve | docs-check'

# Documentation tooling is isolated from the Go framework and client apps.
.PHONY: docs-install docs-dev docs docs-serve docs-check
docs-install:
	npm --prefix docs ci --no-fund

docs-dev: docs-install
	npm --prefix docs start

docs: docs-install
	npm --prefix docs run build

docs-serve:
	npm --prefix docs run serve

docs-check: docs-install
	go test ./docs/...
	npm --prefix docs run check

# Contributor workspace checks; public modules are independently tested below.
MODULE_PACKAGES := ./... ./admin/... ./async/... ./connectors/postgres/... ./connectors/redis/... ./async/redis/... ./tests/integration/...

test: test-js
	go test -race $(MODULE_PACKAGES)

# Contributor-only JavaScript contracts; no browser, npm install or module dependency.
test-js:
	node --test admin/tests/*.test.cjs

vet:
	go vet $(MODULE_PACKAGES)

build:
	go build $(MODULE_PACKAGES)

# Isolated module packaging and generated-client compatibility.
test-modules:
	go run ./scripts/test-modules

# Explicit network-backed security audit; pin the scanner, not its live database.
# The scanner remains a contributor tool, outside all public module requirements.
audit-dependencies:
	go run golang.org/x/vuln/cmd/govulncheck@v1.7.0 -show verbose $(MODULE_PACKAGES)

# Real services in owned disposable fixtures; unsupported tooling fails this gate.
test-integration:
	GOGO_TEST_REQUIRE_SERVICES=1 go test -race -count=1 ./tests/integration/...
