SHELL := /bin/bash

.DEFAULT_GOAL := help

# AgentFlow architecture review
-include .agentflow/agentflow.mk

.PHONY: help test test-js test-modules test-integration audit-dependencies vet build
help:
	@printf '%s\n' \
		'Gogo contributor commands' \
		'' \
		'Usage: make <command> [VARIABLE=value]' \
		'Run make without a command to show this help.'
	@printf '\n%s\n' 'Build and quality:'
	@printf '  %-22s %s\n' \
		'build' 'Compile all workspace modules' \
		'vet' 'Run Go static analysis across workspace modules' \
		'audit-dependencies' 'Scan Go dependencies for vulnerabilities (requires network)'
	@printf '\n%s\n' 'Tests:'
	@printf '  %-22s %s\n' \
		'test' 'Run Admin JavaScript tests and Go tests with the race detector' \
		'test-js' 'Run Admin JavaScript tests only' \
		'test-modules' 'Check isolated module packaging and generated-client compatibility' \
		'test-integration' 'Run integration tests with required PostgreSQL and Redis services'
	@printf '\n%s\n' 'Documentation:'
	@printf '  %-22s %s\n' \
		'docs-dev' 'Install dependencies and start docs at http://127.0.0.1:3000' \
		'docs' 'Install dependencies and build the documentation site' \
		'docs-serve' 'Preview built docs at http://127.0.0.1:3000 (run make docs first)' \
		'docs-check' 'Install dependencies; test docs and Go examples; build and check links' \
		'docs-install' 'Install pinned documentation dependencies'
	@printf '\n%s\n' 'Architecture (AgentFlow):'
	@printf '  %-22s %s\n' \
		'agentflow' 'Render architecture documentation (optional PROPOSAL=<proposal-id>)' \
		'agentflow-help' 'Show AgentFlow command usage'
	@printf '\n%s\n' 'Help:'
	@printf '  %-22s %s\n' 'help' 'Show this command reference'
	@printf '%s\n' \
		'' \
		'Examples:' \
		'  make docs-dev' \
		'  make test' \
		'  make agentflow PROPOSAL=proposal-id'

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
