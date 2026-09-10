#!/bin/sh
set -eu

case "${1:-runserver}" in
    setup) exec /app/docker/setup.sh ;;
    healthcheck) exec wget -q -T 4 -O /dev/null http://127.0.0.1:8000/health/ready/ ;;
esac

read_secret() {
    path="/run/showcase/$1"
    [ -f "$path" ] && [ ! -L "$path" ] && [ -r "$path" ] || {
        echo 'Local credentials are missing; run docker compose up first.' >&2
        exit 1
    }
    value=$(cat "$path")
    [ "${#value}" -eq 64 ] || { echo 'Invalid local credential.' >&2; exit 1; }
    case "$value" in *[!0-9a-f]*) echo 'Invalid local credential.' >&2; exit 1 ;; esac
    printf '%s' "$value"
}

# These belong to the local Compose profile, not the host's optional .env.
GOGO_SECRET_KEY=$(read_secret signing-key)
database_password=$(read_secret database-password)
GOGO_DATABASE_URL="postgres://gogo:${database_password}@127.0.0.1:5432/gogo_showcase?sslmode=disable"
export GOGO_SECRET_KEY GOGO_DATABASE_URL
unset database_password

case "${1:-runserver}" in
    initialize)
        child=
        trap '[ -z "$child" ] || kill -TERM "$child" 2>/dev/null; wait; exit 143' TERM INT
        for command in check migrate seed; do
            /app/manage "$command" &
            child=$!
            wait "$child"
            child=
        done
        echo 'Initialization complete. Create an Admin account with: docker compose run --rm --no-deps web createadmin'
        ;;
    createadmin)
        GOGO_SHOWCASE_ADMIN_IDENTIFIER=admin
        GOGO_SHOWCASE_ADMIN_PASSWORD="Gogo-$(read_secret admin-password)"
        export GOGO_SHOWCASE_ADMIN_IDENTIFIER GOGO_SHOWCASE_ADMIN_PASSWORD
        exec /app/manage "$@"
        ;;
    credentials)
        # Explicit operator-only disclosure, never emitted during startup/logs.
        initial_password=$(read_secret admin-password)
        printf 'Admin username: admin\nInitial Admin password: Gogo-%s\n' "$initial_password"
        ;;
    *) exec /app/manage "$@" ;;
esac
