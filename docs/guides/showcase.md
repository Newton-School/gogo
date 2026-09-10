# Run the showcase

The showcase is the quickest way to explore Gogo: one local application with PostgreSQL models, migrations, Admin, public APIs, forms, field inventories, Redis cache/sessions and a separate Async worker. It consumes the published alpha modules, not workspace replacements.

## Start locally

From a checkout of this repository:

```sh
cd examples/showcase
docker compose up --build -d --wait
```

The first build needs network access. Startup generates private random credentials, starts the databases, runs a separate check/migrate/seed job, then starts web and worker processes. No host `.env` editing is required for this Docker path.

| URL | What to try |
| --- | --- |
| [Home](http://localhost:8000/) | Navigate the sample |
| [Fields](http://localhost:8000/fields/) | Inspect model/form/widget support and limitations |
| [Forms](http://localhost:8000/forms/) | Submit valid and invalid form values; no persistence |
| [Products API](http://localhost:8000/api/v1/products/) | Published-only, read-only catalog |
| [OpenAPI](http://localhost:8000/api/schema/) | Inspect the declared API |
| [Admin](http://localhost:8000/admin/) | Sign in after creating the administrator |

## Create the administrator

```sh
docker compose run --rm --no-deps web createadmin
docker compose run --rm --no-deps web credentials
```

The second command explicitly displays bootstrap credentials. Do not share its output. Re-running `createadmin` refuses to overwrite an existing account.

## Submit background work

```sh
docker compose run --rm --no-deps web demoasync task
docker compose run --rm --no-deps web demoasync group
docker compose run --rm --no-deps web demoasync chain
docker compose run --rm --no-deps web demoasync chord
```

The independently running worker consumes these tasks. A submission timeout is not a revocation of accepted work.

## Stop or rebuild

`docker compose down` stops the sample while preserving its named volumes. Re-run `docker compose up --build -d --wait` to rebuild and start it. Removing volumes destroys sample data and credentials; do not remove only the credential volume while retaining the database.

> This is a local development profile, not a production scaling template. The alpha's development Redis checks require loopback, so the services share a container network namespace. Only the local HTTP port is published.

## Find the implementation

The full [showcase instructions](https://github.com/Newton-School/gogo/blob/master/examples/showcase/README.md) cover native setup, ports, credentials and integration checks. Its [coverage map](https://github.com/Newton-School/gogo/blob/master/examples/showcase/COVERAGE.md) distinguishes real persistence, executable recipes, compile-only examples and descriptor-only fields. Those are different evidence levels, not interchangeable claims of support.
