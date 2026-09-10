#!/bin/sh
# Generate once; never rotate credentials merely because Compose was restarted.
set -eu
umask 077
secret_dir=${1:-/run/showcase}
if [ -L "$secret_dir" ]; then
    echo 'Refusing a symlinked credential directory.' >&2
    exit 1
fi
mkdir -p "$secret_dir"
chmod 700 "$secret_dir"

validate() {
    [ -f "$1" ] && [ ! -L "$1" ] || return 1
    value=$(cat "$1")
    [ "${#value}" -eq 64 ] || return 1
    case "$value" in *[!0-9a-f]*) return 1 ;; esac
}

for name in database-password signing-key admin-password; do
    destination="$secret_dir/$name"
    if [ ! -e "$destination" ] && [ ! -L "$destination" ]; then
        if [ -e "$secret_dir/initialized" ]; then
            echo 'A previously initialized credential is missing; refusing automatic replacement.' >&2
            exit 1
        fi
        temporary=$(mktemp "$secret_dir/.${name}.XXXXXX")
        trap 'rm -f "$temporary"' EXIT HUP INT TERM
        od -An -N32 -tx1 /dev/urandom | tr -d ' \n' > "$temporary"
        validate "$temporary" || { echo 'Credential generation failed.' >&2; exit 1; }
        # Hard-link publication cannot replace an existing credential, even if
        # another explicit setup invocation generated the same file concurrently.
        ln "$temporary" "$destination" 2>/dev/null || true
        rm -f "$temporary"
        trap - EXIT HUP INT TERM
    fi
    validate "$destination" || { echo 'Invalid existing credential; refusing automatic replacement.' >&2; exit 1; }
    chmod 600 "$destination"
    if [ "$(id -u)" = 0 ]; then chown 10001:10001 "$destination"; fi
done
if [ -L "$secret_dir/initialized" ]; then
    echo 'Refusing a symlinked initialization marker.' >&2
    exit 1
fi
touch "$secret_dir/initialized"
chmod 600 "$secret_dir/initialized"
if [ "$(id -u)" = 0 ]; then chown 10001:10001 "$secret_dir/initialized"; fi
if [ "$(id -u)" = 0 ]; then chown 10001:10001 "$secret_dir"; fi
echo 'Local credentials are ready; existing values were preserved.'
