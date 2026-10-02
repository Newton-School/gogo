.DEFAULT_GOAL := help
.PHONY: help build test migrate seed api reports worker report

# This example exercises unreleased source in the contributor checkout.
export GOWORK := $(abspath ../../go.work)

help:
	@printf '%s\n' 'Independent services example' '' 'Commands:'
	@printf '  %-12s %s\n' 'build' 'Build manage, api, reports and worker binaries' 'test' 'Run example tests' 'migrate' 'Apply shared migrations once' 'seed' 'Insert demonstration products' 'api' 'Serve API on port 8000' 'reports' 'Serve reports on port 8001' 'worker' 'Consume report tasks' 'report' 'Read DB and API, dispatch task, await result'

build:
	go build -trimpath -o bin/manage .
	go build -trimpath -o bin/api ./services/api
	go build -trimpath -o bin/reports ./services/reports
	go build -trimpath -o bin/worker ./services/worker

test:
	go test -race ./...

migrate:
	go run manage.go migrate

seed:
	go run manage.go seed

api:
	go run ./services/api serve --addr=127.0.0.1:8000

reports:
	go run ./services/reports serve --addr=127.0.0.1:8001

worker:
	go run ./services/worker worker

report:
	go run ./services/reports report
