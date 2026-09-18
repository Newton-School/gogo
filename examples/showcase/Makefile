.PHONY: check test build migrate seed serve worker

# The checkout sample follows the local framework, including unreleased changes.
export GOWORK := $(abspath ../../go.work)
export GOFLAGS := -p=1

check:
	go run manage.go check
	go run manage.go makemigrations catalog --check
	go run manage.go makemigrations fieldlab --check

test:
	go test -race ./...

build: check
	go build -trimpath -o bin/manage .

migrate:
	go run manage.go migrate

seed:
	go run manage.go seed

serve:
	go run manage.go runserver

worker:
	go run manage.go worker
