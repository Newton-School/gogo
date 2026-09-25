# Install Gogo and prepare your machine

Choose one starting point. You do not need Node.js, Python, or the documentation toolchain to build a Gogo application.

| Starting point | Install locally | Next step |
| --- | --- | --- |
| Explore a complete application | Git and Docker with Compose v2 | [Run the example](showcase.md) |
| Create your own application | Go 1.26.8+, PostgreSQL 16+ or Docker | [Create a project](quickstart.md) |
| Read the docs locally | Go, Node.js 20+, npm, Python 3.10+ | Follow the repository's documentation README |

Redis is needed only for the integrations that select it. Admin and Async are optional. The first tutorial uses only Core and PostgreSQL.

## Check your tools

```sh
go version
git --version
docker compose version
```

The Docker command is only needed for the Docker paths. Run `go env GOBIN GOPATH` if you need to find where Go installs executables; add the explicit GOBIN, or the `bin` directory under GOPATH when GOBIN is empty, to your shell's PATH.

## Install the project generator

```sh
go install github.com/Newton-School/gogo/cmd/gogo@v1.0.0-alpha.2
gogo version
gogo help
```

Expected version: `1.0.0-alpha.2`. Pin this version: `@latest` can select the incompatible older stable product. The first install needs access to the Go module proxy or your configured dependency mirror.

The generator is a development tool. A built application does not need the `gogo` command or Go installed on its server.

## Create a dedicated local PostgreSQL database

Use a database owned by this tutorial, never an existing production database. If you already have PostgreSQL, create a user and empty database through your usual database tools. You will need its connection URL.

For Docker, create the application first using the next chapter, then save this as `compose.yaml` **inside that client project**:

```yaml
name: gogo-storefront
services:
  db:
    image: postgres:18-alpine
    environment:
      POSTGRES_USER: storefront
      POSTGRES_DB: storefront
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:?Set POSTGRES_PASSWORD in .env}
    ports:
      - "127.0.0.1:5432:5432"
    volumes:
      - database:/var/lib/postgresql
    healthcheck:
      test: [CMD-SHELL, "pg_isready -U storefront -d storefront"]
      interval: 3s
      timeout: 3s
      retries: 20
volumes:
  database:
```

Add a **Local database container** group to the generated `.env` and `.env.example`. Put `POSTGRES_PASSWORD=` in the example, and a private random hex password in `.env`. A password manager or `openssl rand -hex 32` can generate one. Do not reuse it elsewhere or commit it. Compose uses this variable; Gogo ignores non-`GOGO_` keys.

```sh
docker compose up -d db --wait
```

Expected: `db` is healthy. In the existing **Database** group of `.env`, set `GOGO_DATABASE_URL` to `postgres://storefront:YOUR_GENERATED_HEX_PASSWORD@127.0.0.1:5432/storefront?sslmode=disable`. Substitute the actual password; Gogo's dotenv loader does not expand `${POSTGRES_PASSWORD}` inside this value. Keep `GOGO_DATABASE_URL` empty in `.env.example`.

The published database port is loopback-only. This plaintext connection is for local development. [Production connectors](connectors.md) require verified transport. If port 5432 is already occupied, change the host side to `127.0.0.1:5433:5432` and use 5433 in the native application URL.

## Confirm the installation

After [creating your project](quickstart.md), run:

```sh
go run manage.go version
go run manage.go check
go run manage.go migrate --plan
```

`check` validates the selected settings and configured checks; it is not proof that every external service is healthy. A migration command is the next database-backed check. If it fails, verify that the database is healthy and that the URL names the correct database and credentials.

Next: [create a basic project](quickstart.md), then [run and compile it](running.md).
