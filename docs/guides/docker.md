# Run your application with Docker

This page packages the `storefront` client from the [product API tutorial](tutorial-api.md). If you want an already complete example with Admin and workers, use [the showcase](showcase.md) instead.

## Files to add

Create these files beside your client's `manage.go`. They are not framework-repository files. Commit `Dockerfile`, `compose.yaml`, `.dockerignore`, migration source, and generated Go descriptors; never commit `.env`.

### Dockerfile

{{code docs/snippets/storefront/Dockerfile}}

The first stage compiles Go. The final image contains the executable and CA certificates, runs as a non-root user, and receives settings at runtime. The tutorial uses image version tags for readability; pin reviewed image digests and refresh security patches in a production build.

### .dockerignore

{{code docs/snippets/storefront/.dockerignore}}

This file matters: excluding `.env` from Git alone does **not** exclude it from a Docker build context.

### compose.yaml

Replace the database-only Compose file from [installation](installation.md) with:

{{code docs/snippets/storefront/compose.yaml}}

The project and volume names match the earlier database-only setup, so the same local database is retained. The `migrate` service must complete successfully before `web` starts. Later deployments should run migration jobs under their own operational controls rather than assuming this local topology is production-ready.

## Configure the private password

Keep the **Local database container** group in `.env` with `POSTGRES_PASSWORD` set to your private generated hex value. Keep the same key empty in `.env.example`. Compose refuses to start if it is missing. Do not use a documented/shared default password.

The native `GOGO_DATABASE_URL` in `.env` uses `127.0.0.1`. Compose explicitly supplies a different URL using `db`, the database's service name. Inside a normal container, `127.0.0.1` means that same container, not your host and not the PostgreSQL service.

| Caller | Address |
| --- | --- |
| Your browser | `http://localhost:8000` |
| Native Go process → database | `127.0.0.1:5432` |
| Web/migration container → database | `db:5432` |
| HTTP listener inside web | `0.0.0.0:8000`, published on host loopback only |

## Build and start

```sh
docker compose up --build -d
docker compose ps -a
docker compose logs migrate web
docker compose run --rm --no-deps web seed
curl -i http://localhost:8000/api/products/
```

Expected: the database is healthy, `migrate` exits with code 0, `web` stays running, and the request returns the published Notebook. The migration exit status matters; logs containing some progress do not prove success.

## Daily commands

```sh
# Rebuild after code changes; Go source is not mounted into this image.
docker compose up --build -d

# Run an explicit management command using the same image/settings.
docker compose run --rm --no-deps web showmigrations

# Stop containers and networks, keeping the named database volume.
docker compose down
```

Do not change the password while reusing an initialized PostgreSQL volume and expect Compose to change the database user's password: initialization variables apply when the database is first created. Rotate the actual user password deliberately. Deleting the volume destroys the data; `down -v` is not the normal stop command.

## Adding Redis and workers

Do not simply add a Redis hostname and assume Async is wired. Install the connector and Async modules, register task/worker factories, and choose roles. This alpha's development Redis contract requires loopback; the [showcase Compose file](../../examples/showcase/compose.yaml) deliberately shares a network namespace to satisfy it. Use that verified local profile for the full stack, or configure authenticated TLS for production. The PostgreSQL-only file above does not add Redis or workers.

Next: [deployment and process roles](deployment.md), [health checks](observability.md), and [troubleshooting](troubleshooting.md).
