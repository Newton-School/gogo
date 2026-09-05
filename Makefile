SHELL := /bin/bash

.DEFAULT_GOAL := help

# AgentFlow architecture review
-include .agentflow/agentflow.mk

.PHONY: help test test-modules test-integration vet build
help: agentflow-help
	@echo 'Product: make test | test-modules | test-integration | vet | build'

# Contributor workspace checks; public modules are independently tested below.
MODULE_PACKAGES := ./... ./admin/... ./async/... ./connectors/postgres/... ./connectors/redis/... ./async/redis/... ./tests/integration/...

test:
	go test -race $(MODULE_PACKAGES)

vet:
	go vet $(MODULE_PACKAGES)

build:
	go build $(MODULE_PACKAGES)

# Isolated module packaging and generated-client compatibility.
test-modules:
	go run ./scripts/test-modules

# Real services in owned disposable fixtures; unsupported tooling fails this gate.
test-integration:
	GOGO_TEST_REQUIRE_SERVICES=1 go test -race -count=1 ./tests/integration/...
