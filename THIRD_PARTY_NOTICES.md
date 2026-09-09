# Third-party notices — v1.0.0-alpha.1

Gogo retains its MIT license in `LICENSE`. The five nested public modules carry
the same license so their source archives remain self-contained. Dependencies
retain their own copyright notices and license terms; Gogo does not relicense
them.

## Manifest dependency inventory

This inventory covers every third-party module explicitly required, directly or
indirectly, by the six public Gogo `go.mod` files for this release. Versions and
license identifiers were checked against those manifests and the downloaded
modules' root license files. Links point to the corresponding upstream version
or commit. A particular application includes only its selected packages.

| Module | Version | License | Upstream license and copyright holder |
| --- | --- | --- | --- |
| `github.com/coder/websocket` | `v1.8.15` | ISC | [Copyright 2025 Coder](https://github.com/coder/websocket/blob/v1.8.15/LICENSE.txt) |
| `github.com/cespare/xxhash/v2` | `v2.3.0` | MIT | [Copyright 2016 Caleb Spare](https://github.com/cespare/xxhash/blob/v2.3.0/LICENSE.txt) |
| `github.com/jackc/pgpassfile` | `v1.0.0` | MIT | [Copyright 2019 Jack Christensen](https://github.com/jackc/pgpassfile/blob/v1.0.0/LICENSE) |
| `github.com/jackc/pgservicefile` | `v0.0.0-20240606120523-5a60cdf6a761` | MIT | [Copyright 2020 Jack Christensen](https://github.com/jackc/pgservicefile/blob/5a60cdf6a761/LICENSE) |
| `github.com/jackc/pgx/v5` | `v5.10.0` | MIT | [Copyright 2013–2021 Jack Christensen](https://github.com/jackc/pgx/blob/v5.10.0/LICENSE) |
| `github.com/jackc/puddle/v2` | `v2.2.2` | MIT | [Copyright 2018 Jack Christensen](https://github.com/jackc/puddle/blob/v2.2.2/LICENSE) |
| `github.com/redis/go-redis/v9` | `v9.22.0` | BSD-2-Clause | [Copyright 2013 The github.com/redis/go-redis Authors](https://github.com/redis/go-redis/blob/v9.22.0/LICENSE) |
| `go.uber.org/atomic` | `v1.11.0` | MIT | [Copyright 2016 Uber Technologies, Inc.](https://github.com/uber-go/atomic/blob/v1.11.0/LICENSE.txt) |
| `golang.org/x/crypto` | `v0.56.0` | BSD-3-Clause | [Copyright 2009 The Go Authors](https://github.com/golang/crypto/blob/v0.56.0/LICENSE) |
| `golang.org/x/sync` | `v0.22.0` | BSD-3-Clause | [Copyright 2009 The Go Authors](https://github.com/golang/sync/blob/v0.22.0/LICENSE) |
| `golang.org/x/sys` | `v0.47.0` | BSD-3-Clause | [Copyright 2009 The Go Authors](https://github.com/golang/sys/blob/v0.47.0/LICENSE) |
| `golang.org/x/text` | `v0.41.0` | BSD-3-Clause | [Copyright 2009 The Go Authors](https://github.com/golang/text/blob/v0.41.0/LICENSE) |

## Distribution scope

- Dependencies are fetched as Go modules, not vendored into Gogo. Their module
  archives contain the original license files and any package-specific notices.
  Preserve those files and notices when redistributing dependency source or
  binaries; this inventory does not replace their full license text.
- `go.sum` may additionally record upstream test, tool, or unimported-package
  dependencies. This table is not a software bill of materials for every
  possible consumer binary or development tool. Generate that inventory from
  the exact build and dependency graph being distributed.
- The Go compiler and standard library have their own
  [Go 1.26.8 license](https://github.com/golang/go/blob/go1.26.8/LICENSE) and
  per-source notices. They are not included in the module inventory above.
- PostgreSQL and Redis server installations are external services, not bundled
  artifacts. The `go-redis` client license above is not a Redis server license.
- This document records dependency provenance, not a compatibility, security,
  or complete Django/Celery parity claim.
