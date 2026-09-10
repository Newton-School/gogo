SHELL := /bin/bash

.DEFAULT_GOAL := help

# AgentFlow architecture review
-include .agentflow/agentflow.mk

.PHONY: help test test-js test-modules test-integration audit-dependencies vet build
help: agentflow-help
	@echo 'Product: make test | test-js | test-modules | test-integration | audit-dependencies | vet | build'
	@echo 'Documentation: make docs | docs-check'

# Public documentation; no web server, npm packages, or framework runtime needed.
.PHONY: docs docs-check
docs:
	python3 docs/build.py

docs-check:
	python3 -m unittest discover -s docs/tests -p 'test_*.py'
	go test ./docs/...
	node --check docs/assets/site.js
	node --test docs/tests/*.test.cjs
	python3 docs/build.py

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
