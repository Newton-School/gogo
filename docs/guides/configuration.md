# Configuration

Keep configuration in your **client project**, not in the Gogo framework repository. Generated applications include `.env` and `.env.example`; the framework itself does not need runnable application credentials.

Use the [environment variable reference](settings.md) for built-in settings and the [Definition reference](options-core-conf-definition.md) for every option available when declaring your own setting. Feature-specific Go configuration belongs to its feature's sidebar, including [PostgreSQL](options-connectors-postgres-config.md), [Redis](options-connectors-redis-config.md), [Admin](options-admin-config.md) and [Async](options-async-clientconfig.md).

## The minimum

| What your application enables | Required settings | What they do |
| --- | --- | --- |
| Fresh generated PostgreSQL project | `GOGO_DATABASE_URL` | Connects to your PostgreSQL server and database. |
| Signing, sessions or authentication resources | Add `GOGO_SECRET_KEY` | Signs application data and session cookies; use a secret of at least 32 bytes. |
| Redis resource | Add `GOGO_REDIS_URL`, such as `redis://127.0.0.1:6379/1` | Connects to Redis and selects the application's database through the URL. |
| SMTP resource | Add `GOGO_SMTP_HOST` and `GOGO_MAIL_FROM` | Selects the email server and outgoing sender address. |
| Local files resource | Add `GOGO_STORAGE_ROOT` | Locates the private directory where uploaded files are stored. |
| Static collection resource | Add `GOGO_STATIC_ROOT` | Selects the output directory for collected static assets. |
| Project-provided reset-delivery resource | Add `GOGO_RESET_DELIVERY_KEY_REF` | Identifies its encryption key; this reserved setting does not enable a delivery service by itself. |

These requirements are resource-scoped. A custom project that selects no external resources can have no required environment values. Secret strength, connection security and other semantic checks also happen at the owning service's construction boundary.

## Defaults and precedence

Management loads the project's `.env`, overlays `Project.Environment`, then overlays the process environment. Schema defaults apply to keys that are absent. An explicitly present empty value is not the same as an absent value: it can replace a default and fail validation.

`GOGO_ENV` defaults to `development`; supported values are `development`, `test`, and `production`. Debug defaults to false. HTTP listens on `127.0.0.1:8000` unless changed.

Read values through `conf.Values` using `String`, `Bool`, `Int`, `Duration`, `List`, and `Secret`. The secret wrapper redacts formatting and JSON; call `Reveal` only at a trusted boundary such as opening a connection. Never print the revealed value.

## Add an application setting

Append a `conf.Definition` to `conf.CoreSchema()` in your project's `Settings()` function. Declare its name, type, default, allowed values and `RequiredFor` resource names. Names must begin with `GOGO_`.

```go
schema := conf.CoreSchema()
schema = append(schema, conf.Definition{
    Name: "GOGO_CATALOG_PAGE_SIZE",
    Group: "Catalog",
    Kind: conf.Integer,
    Default: "25",
    Min: 1,
})
```

Unknown `GOGO_*` settings are rejected. This catches misspellings, but also means that adding `GOGO_MODES` today without declaring it will not enable runtime modes.

## Validation is not compilation

`go build` compiles Go; it cannot require secrets that should remain outside the binary. Run the project's management checks and configuration-aware build command as appropriate. Missing required configuration must stop the relevant management/runtime operation before opening its resources.

Use `go run manage.go help` to discover the commands your project actually registers. A setting in the Core template does not prove that your project has wired the corresponding mail, storage, session or account service.

## Production configuration

Set explicit allowed hosts and trusted proxy networks. Use the connector's required TLS, authentication and durability options. Assign a dedicated Redis database per application/environment through the URL; all server, worker and scheduler processes sharing state must use the same database. Logical databases prevent key collisions, not unauthorized access or shared-server resource contention. Do not place production credentials in `.env.example`, container images, source code, command-line flags or logs.

Redis configuration has no application namespace or automatic application prefix. See [database selection and the upgrade boundary](connectors.md#redis-databases) before switching an existing application. This change is in the checkout, not the already published `v1.0.0-alpha.1` modules.

The [complete settings reference](settings.md) is generated directly from `conf.CoreSchema()` so defaults and required-resource names stay synchronized.
