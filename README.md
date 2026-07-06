# Gogo

## Download

Install the CLI from the published release:

```bash
go install github.com/Newton-School/gogo/cmd/gogo@v0.2.0
```

Use the framework as a Go module:

```bash
go get github.com/Newton-School/gogo@v0.2.0
```

Prebuilt CLI binaries and checksums are available from GitHub Releases:

https://github.com/Newton-School/gogo/releases

If a newly published tag is not available from the public Go checksum database
yet, install directly from Git:

```bash
GOPROXY=direct GONOSUMDB=github.com/Newton-School/gogo \
  go install github.com/Newton-School/gogo/cmd/gogo@v0.2.0
```

## Setup

Requirements:

- Go `1.26.4` or newer.
- Git.

Clone the repository:

```bash
git clone git@github.com:Newton-School/gogo.git
cd gogo
go mod download
```

Prepare local environment configuration:

```bash
cp .env.example .env
```

Keep `.env` out of Git. Commit `.env.example` only.

Build and test the local checkout:

```bash
make build
make test
```

## Contributing

Read `CONTRIBUTING.md` before opening changes.

AI-assisted contributors must also follow `AGENTS.md` and the
workflow-specific rules in `.agent/rules/`.

Common verification commands:

```bash
make test
make docs-verify
make ci
```

Report security issues privately through GitHub Security Advisories and follow
`SECURITY.md`.

## License

Gogo is released under the MIT License. See `LICENSE`.
