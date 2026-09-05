# Implementation progress — not completion evidence

Plan: `gogo-framework-end-to-end`  
Approved architecture: `5b3383ff4fa95ff74897e635f1033351ca799444d7147a44e5e91eae0256d30b`

This is an execution ledger, not a requirements document or an AgentFlow completion record. The approved architecture and immutable manifest remain authoritative. Every task below remains open until all its branches and consumers have verified implementation evidence. Catalog entries and a passing package test are not parity proof.

## Verified checkpoints so far

- Explicit HTTP create receipts now require one bounded operation key unless optional mode is deliberately selected. Stable actor/scope/model-contract identity binds method, host, path, parsed input and application Vary semantics; current add/object/row/relation/field authority is reapplied to both creation and replay. Eight concurrent requests produce one row/audit/receipt; body/path/host/Vary conflicts, current redaction/grant loss, hidden roots, late root/target mutations, unknown commits and after-commit failures have actual PostgreSQL tests. Independent review drove pre-transaction JSON marshaler panic containment and truthful pre-commit callback rollback reporting. Physical retention cleanup and update/delete receipt integration remain open.
- The immutable `24fe303bcc65311b43105139c3275283d968e667` snapshot passed isolated installation of all six public modules and the fresh generated consumer, strict PostgreSQL/Redis integration (180.831s), vet and build. This supersedes the earlier `8dd0f1a` full gate; subsequent commits have focused checks and still need a fresh coherent full gate. No module publication or deployed-product proof is implied.
- Chrome review of a disposable scoped staff fixture confirmed user identifier/direct-permission edits, group name/permission edits, disabled-password account creation and privileged password disabling. PostgreSQL metadata and four redacted audit rows independently confirmed the synthetic changes. The group editor was inspected at 390px width with no horizontal page overflow. UUID-heavy headings and cramped credential text drove scoped labels, an opt-in escaped actor label and shared form spacing regressions. Chrome explicitly blocked history navigation and password-reset POST; neither flow is claimed browser-verified and no bypass was attempted. The owned fixture/services were stopped and its schema removed; shared PostgreSQL was preserved.
- ModelForm now retains operational validation failures separately from user-correctable field errors; Admin parent/inline consumers return safe service failure instead of a misleading validation response. Independent source, race and actual PostgreSQL regressions cover joined provider/cancellation errors and inline rollback. This follows the earlier pure-validation classifier checkpoint.
- Implicit per-model aggregate annotations preserve distinct root identities, typed/null/JSON outputs, pagination and scoped eager reads; explicit grouping remains available. Automatic HAVING routing is a separate reviewed follow-up. Worker startup now renews its lease during task-start observation and confirms the fence before handler entry; the reproduced expired-lease entry fails before the fix and passes afterward. Beat calculations reject overflowing cron steps, unportable instants and nonadvancing custom calendars before occurrence construction, with real Redis recovery tests. These remain focused catalog slices, not full ORM/worker/scheduler conformance.
- Explicit typed JSON resource creation now runs model normalization after hooks/defaults/auto fields but before SQL encoding, then enforces readonly write authorization, scoped/locked relation targets, immutable inserted identity, safe representation and same-transaction audit. Callable UUID defaults, hidden/missing target equivalence, nested policy/audit mutation rollback, cookie CSRF and confirmed/unknown commit outcomes have actual PostgreSQL regressions. Independent review repaired pre-clean values reaching INSERT and foreign transaction errors substituting for the outer outcome. HTTP idempotency integration, update/delete and full write conformance remain pending.
- ORM Save has an explicit per-attempt mutable Prepare lifecycle without making ordinary Save automatically call FullClean. Provider errors and cancellation now survive model validation rather than becoming public field messages; bounded pure-validation classification protects API/HTTP responses from joined provider failures. Preparation, raw/update-fields/fallback/parent boundaries and single-encoding regressions pass. ModelForm operational-error propagation is a separate consumer follow-up in progress.
- Typed row annotations and explicit GroupBy/Having queries support scoped joins, filtered/distinct aggregates, typed output maps and counts. Reviews repaired transformed aggregate aliases reaching WHERE, alias cycles and output-identity mismatches; PostgreSQL also rejects names over its 63-byte identifier limit before silent truncation can collide. Implicit per-model grouping, automatic HAVING routing, windows and subquery conformance remain open.
- Default UserAdmin and GroupAdmin now include scoped group/direct-permission selectors with readonly/exclusion support, signed visible-selection snapshots and atomic redacted audit. Hidden links are retained server-side only under complete domain authority; submitted choices are rechecked after hooks. Custom stock grant overrides fail explicitly rather than being ignored. Independent code and PostgreSQL HTTP reviews pass, including unknown commits, stale edits and provider denial. Fresh Chrome review is pending.
- Async monitoring now offers explicit scoped event pages and lazy cancellable observation with stream and per-event current authorization. Queue/schedule, worker and progress observations are optional and lossy; relay, delayed and outbox observers run only after the corresponding source acknowledgement. Observer failures cannot substitute for durable outcomes. Independent race and actual PostgreSQL/Redis checks cover source-ack ordering, callback failures and payload redaction; dashboard and the full monitoring/control catalog remain open.
- Signed forward cursor resources now bind exact ordering/filter/page-size, stable tenant scope, verified actor/grants and resource/API version. Immutable public non-null ordering keys are required; hidden cursor keys fail closed, and current row scope is reapplied on every page. PostgreSQL tests cover earlier inserts, key precision, replay/tamper denial, and both directions for date/time/datetime/duration/decimal/IP positions (1.692s). Reverse navigation and snapshot isolation remain pending. Canonical relative links obey the same bounded query budget as requests.
- API durable operation receipts now have opt-in models/migrations, bounded same-transaction unique claims/locks, exact numeric request digests, current authorization and removal-only response redaction, atomic mutation/receipt rollback and documented expiry reuse. Initial/replayed JSON numeric bodies and object identities remain stable through JSONB. Independent review fixed foreign callback errors incorrectly implying this transaction committed; an explicit outer commit marker gates every response. Race/provider tests cover twelve concurrent callers, conflicts, tenant/user separation, revoked permission, current redaction, expiry, contended-lock timeout, failed serialization, callback panic and lost acknowledgement (latest focused API/actual-PG 1.504/1.858s). Resource HTTP write integration, retention maintenance and full API conformance remain pending.
- PostgreSQL commit-stage context/transport failures are now conservatively unknown, separate from ordinary query cancellation and definite server rollback responses. Injected database/sql provider tests verify classification without claiming a real network-loss experiment. This supports safe same-operation reconciliation in API/auth consumers.
- ORM typed Case/When expressions, exact typed-literal casts and scoped Query.Update now have compiler and PostgreSQL coverage. Update locks/transactions and matched-row counts cover atomic concurrent increments, scoped old-row selection, JSON/null and savepoint rollback; it intentionally bypasses instance save hooks. Shared provider field decoding now hydrates PostgreSQL fixed intervals as exact Go durations across typed records, MapRecord, Values, aggregate, bulk, refresh and eager paths; calendar-month/overflow intervals fail instead of rounding. Custom model codecs retain raw driver-value precedence. These are focused checkpoints, not full ORM conformance.
- Redis workflow repair now includes fixed-partition bounded inventory and explicit-authority coordinator cursors; gaps neither fabricate tasks nor starve later work when the caller retries the retained cursor. Strict lexicographic Results cleanup preserves active pins/tombstones and rejects legacy indexes/cursors without implicit migration. Shared intent transitions preflight all key types and metadata before any mutation. Actual Redis regression and independent review gates cover formerly partial Lua writes, exact revision agreement and cursor/callback safety; broad failover/fairness work remains open.
- Default UserAdmin now supports domain-backed identifier changes and explicit disabled-password creation in addition to flags and password changes. Core identity/group edits use exact delta authority, normalized identifiers, member auth-version invalidation and final persisted snapshot guards; nested extension changes roll back. CreateGroup likewise rechecks its exact grant-free snapshot. PostgreSQL tests cover self-session invalidation, audit rollback, unknown commit and later session-store failure. These new UI surfaces still require current Chrome review; GroupAdmin/grant selectors remain in implementation.
- Core configuration, registry lifecycle, authentication primitives, browser security, sessions, cache, named routing, structured responses and scaffold generation have focused race tests.
- The immutable `8dd0f1a` archive passed workspace-disabled local-proxy tests for all six public modules and a fresh consumer's project/app creation, typed model generation, migration generation, tests and compilation. This coherent rerun supersedes an earlier mixed-concurrent-edit failure. The same snapshot passed strict PostgreSQL/Redis integration (173.315s), vet and build. Newer checkpoints have focused verification and still require another full coherent snapshot gate. These checks verify local packages, not a published release.
- Disposable PostgreSQL integration covers save and validation branches, schema/migration history and rollback, scoped deletion and relational integrity, and scoped Admin writes with atomic audit.
- Disposable Redis integration covers cache/session compare-and-swap, rate limits, fenced task execution, delayed dispatch, durable workflow intents, group/chord results, periodic scheduling and recovery.
- A cross-module PostgreSQL/Redis test covers business/outbox rollback and eventual dispatch.
- Chrome desktop inspection has covered the Admin dashboard, product list and change form. A synthetic product save was verified afterward in PostgreSQL and its audit row. Browser history, inline, responsive and complete accessibility review remain open. Stale assets found in Chrome drove content-hashed asset URLs and cache-policy regressions.
- Independent reviews have identified and driven fixes for session cache headers, Admin deletion scope, audit snapshots, response-write failure handling and scaffold path confinement. Nested multi-alias transaction-context repair passes fake-backend and real PostgreSQL A/B/A rollback regressions; this does not close the complete transaction catalog audit.
- API serializers now have explicit model allowlists, nested/list/dictionary validation, PATCH presence rules, plain/scalar/file codecs, bounded parsers and negotiation. Read-only Resource handlers add named app routes, required request/object/field policies, token ceilings, mandatory root/eager query scopes, typed filter/search/order allowlists and bounded page/offset pagination. Counts and navigation require scope to encode all row visibility; count/page reads are not a consistent snapshot. Actual PostgreSQL tests cover scope, field-computation denial, stable PK ordering, safe detail absence, provider/cancellation classification and no partial responses. Review fixed partial email/URL operands incorrectly running model validators and decoder failures incorrectly becoming 404. Latest focused API/pagination/real-PG race gates pass (1.505/2.031/3.378s). Later checkpoints above add forward cursors and durable receipts; generic HTTP writes, custom actions and schema generation remain pending.
- Shared session/cookie/fallback flash storage has consume-once, plain-text/tag, size/level, privacy, tamper, failed-response and late-header tests. Independent review drove eager cookie-capacity checks and late Peek denial; Admin writes now emit generic success notices after persistence.
- Authenticated-session middleware reloads current grants and account version. Real PostgreSQL accounts and both PostgreSQL/Redis session providers pass the same credential HTTP workflow: CSRF, normalized identity/IP rate gates, exact password whitespace, generic denial, login rotation, safe redirects, logout, privilege-version invalidation and session-write failure. Independent reviews closed current-response CSRF rotation, account-switch flash leakage and mutable trusted-origin settings. Legacy hash catalogs and the complete authentication catalog remain open.
- Self-service password changes verify the current password, lock/recheck the account, preserve immutable validator claims and reject nested callback credential changes. Their HTTP and Admin adapters distinguish unchanged, committed and uncertain outcomes, never promote failed/unknown results, and require explicit current-session preservation. Real PostgreSQL plus both session providers cover exact data preservation, old-session/CSRF invalidation, provider-write failure and actual commit-then-unknown behavior. The independent concurrency review found stale rotation overwrote a competing session save; conditional versioned revocation now rejects that race before creating a replacement. Unsupported third-party session stores fail closed on existing-key rotation. Privileged UserAdmin and transactional security-audit completion remain open.
- Password resets have a separate opt-in migration, canonical purpose/identity-bound 256-bit secrets, digest-only token rows and one-hour expiry. Synchronous delivery resolves an explicitly verified recipient and sends Sensitive mail only after confirmed token commit; generic request acknowledgements and padded deadlines also cover resolver/provider failures and panics. Confirmation locks token then user, atomically consumes the token and replaces the credential, and never returns an authenticated principal. Actual PostgreSQL tests and independent review cover duplicate redemption, expiry, account-version invalidation, policy/callback rollback, after-commit failure and unknown commit. Request/confirmation HTTP forms now pass the full workflow with both PostgreSQL/Redis sessions, including token replay, policy retry, no automatic login and committed password plus session-cleanup failure. The content-hashed script moves a fragment token into the POST form and removes history; a Node VM DOM fixture verifies this behavior, not visual/browser conformance. Encrypted queued delivery, maintenance commands and the complete reset audit remain open.
- Built-in users, groups, permissions and join records have explicit migrations, authorization-before-write guards and persisted-state checks. Concurrent membership/grant tests observe actual PostgreSQL wait graphs and verify whole-transaction rollback on deadlock, explicit retry and version invalidation. Stable content types and scoped generic references have real PostgreSQL identity/version/concurrent synchronization tests. Custom-user relational swapping and the remaining generic-relation catalog are not complete.
- PostgreSQL sessions store only digests of canonical random bearer keys, bounded opaque values inside JSONB, versioned updates, expiry and revocation tombstones. Eight-writer CAS, exact large numbers, stale-create/save denial, bounded cleanup, version exhaustion and uncommitted-transaction rejection pass real PostgreSQL tests. Provider clocks govern expiry; idle-expiry policy and complete session-management commands remain open.
- Startup/shutdown has race-tested ownership, prevalidated resource selection, lifetime-aware readiness, deadline-bounded cleanup and redacted callback-panic handling. Non-cooperative Go callbacks can outlive deadlines; timeout does not prove they stopped. Command cleanup runs on callback failure and panic.
- Bulk create/update, named-constraint upsert, scoped scalar/many-to-many managers, automatic/explicit intermediary schemas and scoped join-row deletion have real PostgreSQL tests. Select-related uses joined SQL; prefetch batches related collections with scoped target queries, bounded finite self paths, nested/custom cache validation and parameter caps. Required unscoped joins participate in row locking; nullable/scoped branches preserve outer rows. Independent reviews and real PostgreSQL tests cover these slices; the complete eager/expression/lookup catalog remains open.
- Typed aggregate outputs now cover scoped sum/average/count/min/max, population/sample variance/deviation, DISTINCT, FILTER and explicit defaults with real PostgreSQL precision/empty-set tests. Sliced/grouped/locked aggregate sources fail explicitly until their subquery path exists. JSON model reads preserve exact numbers and explicit SQL/JSON null distinctions; equality nil matches JSON null, isnull matches SQL NULL, and native strings survive repeated model/form cleaning. Optional public dialects now support parameterized containment/key lookups and key/index paths, including scoped joined paths, text extraction, ordering and typed Values. JSON IN/range distinguishes native strings/null and compiles expression operands; empty predicates remove unused path bindings. Typed slice/map model fields survive cleaning, save and hydration without precision loss or partial assignment on error. Independent review and real PostgreSQL regressions cover these slices; advanced expressions and full lookup parity remain open.
- Admin many-to-many forms preserve hidden existing links without exposing them in choices, tokens or audit. Posted IDs are independently checked, including already-linked IDs rechecked after mutation hooks. Real PostgreSQL tests cover parent/relation/audit rollback and optimistic conflicts. Opt-in branded staff login/logout uses the shared account workflow, requires actual model grants and has PostgreSQL/Redis integration tests. New login, multi-select and autocomplete UI have not yet had Chrome visual review.
- Nested Async canvases persist ordered barriers and dispatch intents. Cancellation preserves active execution leases, and delayed-task replay retention includes the original schedule horizon. Task replacement retains the original logical result until its replacement graph settles; restart, nested workflows, callbacks, cancellation and delayed retention have real Redis tests. Oversized graph coordination settles dispatched children and releases pins without dropping their results. Worker/control/result-retention catalogs remain open.
- Flat task groups now expose ready/success/failure checks, non-propagating ordered outcomes and bounded streaming with per-yield authorization, partial-error handling, worker deadlock guards and authorized child-result recovery. Memory and real Redis tests pass; nested/chain/chord handles are not silently reinterpreted as flat groups. Process-tree cleanup now observes ordinary descendants exiting while the unreaped leader pins group identity; a bounded observation failure reports CHILD_FAILED. Repeated macOS race/stress tests and independent review support the checkpoint; occasional earlier safe cleanup failures are not fully explained. Linux cross-build is not runtime validation; escaped sessions/credentials require external supervision.
- Admin readonly many-to-many display uses a bounded scoped reader across flat forms, fieldsets and inlines; hidden targets/intermediaries never disclose labels, IDs or counts, and readonly forged POST fields do not write. Policy/provider errors and cancellation fail closed instead of silently hiding outages. Safe UserAdmin flags, account creation and privileged password/unusable-password editors use same-store Accounts and atomic redacted audit, with hashless projections, exact delta authority and protected deletion graphs. Review fixed mutable validation callbacks retargeting account identities and repeated identifier normalization. Actual PostgreSQL regressions cover rollback, add-only authority, committed/unknown acknowledgements and committed self-password change plus session-delete failure. Branded reset request/confirmation adapters and a local-only synthetic review demo are committed. Grant/group editors and complete UserAdmin behavior remain open; these new UI paths still require Chrome review.
- Worker presence has immutable public runner instances independent of secret ownership leases. Exact-instance remote shutdown now has bounded Memory/Redis transport, immutable request IDs, per-worker/queue authorization, partial/unknown submission and reply outcomes, restart protection and accepted-work drain. Accepted replies never assert process exit. Independent review found and fixed a malformed-time JSON-clone panic; source review and real Redis race tests cover the integrated SDK/runner flow. Remote control is explicitly opt-in, and no unapproved CLI verb was added. Complete worker scaling/recycling/recovery catalogs remain open.
- Exact-workflow reconciliation repairs missing barrier completions only from matching authoritative terminal task records. It reuses ordinary graph CAS/successor intents, preserves active pins/outcomes, never reruns or fabricates absent tasks, and reports source gaps. Bounded child cursors avoid starvation; per-workflow/task reconciliation authority is rechecked. Independent memory/actual Redis races pass (1.396/1.846s), with concurrent chord repair and cancellation regressions. Later checkpoints above add partition inventory/background coordination; full Redis failover conformance remains open.
- Shared credential-page renderers now drop partial error output and sanitize callback panics across login, password change and both reset forms; cancellation is checked before/after rendering. Independent Auth views/Admin races pass. Custom policy wrappers preserve explicit unconstrained anonymous rules while default ModelPolicy and invalid constrained identities still deny; no token ceiling is widened.
- Token scope ceilings are private, cloned and non-widening across principal contexts, ModelPolicy and custom policy wrappers. Account services enforce mapped model-action ceilings before their exact-delta authorizers; unknown actions deny constrained identities. Session loading, Login and RefreshLogin reject token-to-cookie promotion, and constrained principals cannot lose their ceiling through ordinary JSON. Opaque API tokens have independent models/migrations, digest-only secrets, explicit 24h default expiry, current grant/version checks and authorized issuance/revocation. Bearer HTTP fails closed on malformed/duplicate credentials, provider outage or panic without cookie fallback. Actual PostgreSQL tests and independent review cover concurrent/idempotent revocation, hook and authority rollback, expiry and committed/unknown outcomes without returning unconfirmed secrets. Review fixed callback-only no-op revocation reporting. Full token/JWT/OIDC and audit parity are not complete.
- Core mail now has bounded immutable MIME/envelopes, verified TLS SMTP, bounded pooling and per-recipient accepted/rejected/unknown/simulated outcomes without blind retries. Sensitive messages reject ordinary JSON, console and file output; all-verb formatter regressions protect routine rendering. Memory, console, private root-confined quota-limited file and dummy development backends plus configured administrative recipient helpers have focused and independent tests. Only synthetic local SMTP was contacted. SMTPUTF8, the complete SMTP authentication/transport catalog and encrypted durable delivery remain open.
- Shared flash storage now removes an exhausted queue without creating a blank session after logout. Newly queued anonymous notices still persist deliberately; confirmed retained-login password changes use a generic Admin notice. Session/fallback consume, failed-response and real PostgreSQL/Redis Admin logout regressions pass.
- Redis delayed/periodic/workflow intent payloads now remain opaque through Lua, and fences/revisions use exact decimal counters. Real Redis regressions cover object key order, values beyond 2^53, counter exhaustion and batch preflight before mutation. Cache increment returns exact Redis integer text. Earlier unversioned internal records fail closed; there is no automatic data migration. Independent review caught partial same-partition lease mutation on later-item error; fixed by whole-batch preflight. Separate Cluster partitions are not one atomic transaction.
- The strict cross-module integration gate passed against disposable supported PostgreSQL and Redis, followed by workspace vet and build checks. This is local conformance evidence, not deployment or exhaustive compatibility evidence.

## Verification entry points

- `make test`: race tests for all workspace modules, including integration tests; unavailable optional local service binaries may cause explicit skips.
- `make test-modules`: isolated public-module and fresh-consumer verification with workspace overrides disabled.
- `make test-integration`: require PostgreSQL 16+ tooling and Redis 7.2+ tooling on PATH (or explicitly configured fixture binary directories); tests allocate disposable local services.
- `make vet`: static checks for all workspace modules.

Service availability and skipped tests must always be reported separately from a pass. Neither a successful command nor this ledger marks AgentFlow implemented. Final completion requires the exact implementation audit and completion command.

## Open task coverage

| Task | Approved feature | Current stage |
| --- | --- | --- |
| `map-repository` | Repository roots and independent Go modules | Partial code; full conformance pending |
| `map-scope` | Scope, compatibility and design decisions | Pending implementation or audit |
| `map-products` | Products, packages and runtime roles | Partial code; full conformance pending |
| `runtime-defaults` | Proposed defaults and environment requirements | Partial code; full conformance pending |
| `core-bootstrap` | Start an application and freeze its registry | Partial code; full conformance pending |
| `core-checks` | Run system and deployment checks | Partial code; full conformance pending |
| `core-lifecycle` | Drain requests and shut down resources | Partial code; full conformance pending |
| `core-settings` | Load configuration and validate the build target | Partial code; full conformance pending |
| `core-signals` | Dispatch typed lifecycle and model signals | Partial code; full conformance pending |
| `core-typed-errors` | Translate errors at the public boundary | Partial code; full conformance pending |
| `models-field-catalog` | Model fields, options and exact storage intent | Partial code; full conformance pending |
| `models-inheritance` | Resolve model composition and polymorphic identity | Pending implementation or audit |
| `models-legacy` | Inspect an existing database and map legacy tables | Pending implementation or audit |
| `models-schema` | Declare a model and generate typed accessors | Partial code; full conformance pending |
| `models-validate` | Validate an instance before persistence | Partial code; full conformance pending |
| `orm-bulk` | Create, update and upsert batches | Partial code; full conformance pending |
| `orm-cardinality` | Handle missing, singular and multiple rows | Partial code; full conformance pending |
| `orm-delete` | Collect and delete related objects safely | Partial code; full conformance pending |
| `orm-eager` | Load joins and collections without hidden N+1 queries | Partial code; full conformance pending |
| `orm-expressions` | Evaluate aggregates, expressions and database functions | Partial code; full conformance pending |
| `orm-locking` | Coordinate concurrent writers | Partial code; full conformance pending |
| `orm-native` | Use explicit native SQL and inspect query performance | Partial code; full conformance pending |
| `orm-operation-catalog` | Query, expression and lookup inventory | Partial code; full conformance pending |
| `orm-relations` | Read and mutate related objects | Partial code; full conformance pending |
| `orm-routing` | Route reads, writes and migrations to database aliases | Pending implementation or audit |
| `orm-save` | Insert or update a model with transaction hooks | Partial code; full conformance pending |
| `orm-select` | Build, execute and materialize a query | Partial code; full conformance pending |
| `orm-transactions` | Commit, nest or roll back a transaction | Partial code; full conformance pending |
| `orm-upsert` | Resolve create-or-update races safely | Partial code; full conformance pending |
| `postgres-connect` | Open and manage a PostgreSQL connection alias | Partial code; full conformance pending |
| `postgres-execute` | Compile PostgreSQL SQL and execute it safely | Partial code; full conformance pending |
| `postgres-extensions` | Use PostgreSQL-specific model and query features | Partial code; full conformance pending |
| `postgres-schema` | Execute schema operations and introspect results | Partial code; full conformance pending |
| `migrations-apply` | Apply migrations with locking and durable history | Partial code; full conformance pending |
| `migrations-data` | Run a historical data migration | Partial code; full conformance pending |
| `migrations-detect` | Detect model changes and write a migration | Partial code; full conformance pending |
| `migrations-online` | Expand, backfill and contract an online schema | Pending implementation or audit |
| `migrations-operations` | Migration operation catalog | Partial code; full conformance pending |
| `migrations-reverse` | Preview, reverse or explicitly fake a migration | Partial code; full conformance pending |
| `migrations-squash` | Squash and merge migration history | Pending implementation or audit |
| `redis-async-adapter` | Connect Async contracts to Redis storage roles | Partial code; full conformance pending |
| `redis-atomic` | Run atomic Redis operations with cluster-safe keys | Partial code; full conformance pending |
| `redis-connect` | Open Redis connections by workload role | Partial code; full conformance pending |
| `redis-reconcile` | Reconcile lost acknowledgements and Redis failover | Partial code; full conformance pending |
| `redis-records` | Redis keys, partitions and retention | Partial code; full conformance pending |
| `security-catalog` | Identity, session and security options | Partial code; full conformance pending |
| `security-csrf` | Validate CSRF and origin for cookie-authenticated writes | Partial code; full conformance pending |
| `security-headers` | Apply host, proxy, CORS and browser security policies | Partial code; full conformance pending |
| `security-signing` | Sign and verify time-bounded values | Partial code; full conformance pending |
| `auth-login` | Authenticate and create a session | Partial code; full conformance pending |
| `auth-password-change` | Change a password or privileged user grants | Partial code; full conformance pending |
| `auth-password-reset` | Reset a password without leaking account existence | Partial code; full conformance pending |
| `auth-password-reset-confirm` | Consume a reset token and replace the password | Partial code; full conformance pending |
| `auth-permissions` | Check action, object and field permissions | Partial code; full conformance pending |
| `auth-tokens` | Authenticate a bearer token and rotate credentials | Partial code; full conformance pending |
| `sessions-request` | Load, rotate, save and revoke sessions | Partial code; full conformance pending |
| `http-catalog` | HTTP, middleware and view building blocks | Partial code; full conformance pending |
| `http-generic` | Serve generic list, detail, edit and date views | Pending implementation or audit |
| `http-request` | Receive a request and send a response | Partial code; full conformance pending |
| `http-responses` | Construct JSON, redirects, files and conditional responses | Partial code; full conformance pending |
| `http-routes` | Register, resolve and reverse named routes | Partial code; full conformance pending |
| `http-streaming` | Stream responses and reconnect SSE clients | Partial code; full conformance pending |
| `http-websocket` | Authorize and process WebSocket messages | Pending implementation or audit |
| `forms-bind` | Bind and validate a form | Partial code; full conformance pending |
| `forms-catalog` | Forms, formsets, fields and widgets | Partial code; full conformance pending |
| `forms-formsets` | Validate and save a formset or inline formset | Partial code; full conformance pending |
| `forms-model` | Save model forms without mass assignment | Partial code; full conformance pending |
| `templates-catalog` | Template engine, tags, filters and extension points | Partial code; full conformance pending |
| `templates-render` | Load, inherit and render a template safely | Partial code; full conformance pending |
| `api-catalog` | REST API feature and serializer catalog | Partial code; full conformance pending |
| `api-idempotency` | Claim and replay an idempotent mutation safely | Partial code; full conformance pending |
| `api-pagination` | Filter, search, order and paginate without scope leaks | Partial code; full conformance pending |
| `api-read` | Serve scoped resource lists and details | Partial code; full conformance pending |
| `api-schema` | Generate OpenAPI and typed clients | Pending implementation or audit |
| `api-serializers` | Parse, validate and serialize typed resources | Partial code; full conformance pending |
| `api-write` | Create, update or delete a resource | Partial code; full conformance pending |
| `admin-access` | Authenticate staff and render the Admin index | Partial code; full conformance pending |
| `admin-actions` | Execute a custom action or bulk list edit | Partial code; full conformance pending |
| `admin-autocomplete` | Search related objects and select permitted IDs | Partial code; full conformance pending |
| `admin-delete` | Preview and confirm scoped deletion | Partial code; full conformance pending |
| `admin-form` | Render create, change and read-only forms | Partial code; full conformance pending |
| `admin-history` | Read history and customize Admin behavior | Partial code; full conformance pending |
| `admin-list` | Search, filter and paginate an Admin change list | Partial code; full conformance pending |
| `admin-options` | Admin configuration and hook inventory | Partial code; full conformance pending |
| `admin-register` | Register models and publish an Admin site | Partial code; full conformance pending |
| `admin-save` | Save parent, inlines and audit in one transaction | Partial code; full conformance pending |
| `cache-access` | Read through cache and invalidate after a write | Partial code; full conformance pending |
| `cache-invalidate` | Invalidate derived cache entries after mutation | Pending implementation or audit |
| `ratelimit-consume` | Enforce a distributed request or task quota | Partial code; full conformance pending |
| `files-direct` | Verify direct multipart uploads through a future storage adapter | Pending implementation or audit |
| `files-download` | Authorize download, deletion and signed access | Pending implementation or audit |
| `files-upload` | Receive, validate and persist an upload | Pending implementation or audit |
| `static-collect` | Collect, fingerprint and serve static assets | Pending implementation or audit |
| `email-send` | Build and send a validated email | Partial code; full conformance pending |
| `email-sensitive-delivery` | Deliver encrypted single-use reset mail | Pending implementation or audit |
| `messages-flash` | Store and consume one-time user messages | Partial code; full conformance pending |
| `i18n-locale` | Resolve locale, translate and format values | Pending implementation or audit |
| `fixtures-data` | Export and import structured fixtures | Pending implementation or audit |
| `contrib-admindocs` | Generate developer-facing model and route reference | Pending implementation or audit |
| `contrib-contenttypes` | Resolve model identities and generic relations | Partial code; full conformance pending |
| `contrib-feeds` | Generate RSS or Atom syndication feeds | Pending implementation or audit |
| `contrib-flatpages` | Serve a registered flat page | Pending implementation or audit |
| `contrib-gis-import` | Import, transform and export spatial datasets | Pending implementation or audit |
| `contrib-gis-query` | Declare and query spatial data through PostGIS | Pending implementation or audit |
| `contrib-humanize` | Format human-readable display values | Pending implementation or audit |
| `contrib-redirects` | Apply a site-bound redirect after route miss | Pending implementation or audit |
| `contrib-sitemap` | Generate sitemap indexes and bounded sitemap pages | Pending implementation or audit |
| `contrib-sites` | Resolve the current site and scope site-owned content | Pending implementation or audit |
| `async-beat` | Evaluate periodic schedules and dispatch one occurrence | Partial code; full conformance pending |
| `async-callbacks` | Dispatch callbacks, errbacks and task replacement | Partial code; full conformance pending |
| `async-cancel` | Revoke pending work or cooperatively cancel execution | Partial code; full conformance pending |
| `async-canvas-catalog` | Canvas operations, result composition and scheduling options | Partial code; full conformance pending |
| `async-chain` | Execute a chain and stop on failure | Partial code; full conformance pending |
| `async-chord` | Complete a chord barrier and dispatch its callback | Partial code; full conformance pending |
| `async-contracts` | Broker, result, scheduler and workflow adapter contracts | Partial code; full conformance pending |
| `async-delayed` | Move delayed and retried tasks into runnable queues | Partial code; full conformance pending |
| `async-group` | Fan out a group and collect ordered outcomes | Partial code; full conformance pending |
| `async-intent-relay` | Relay durable task and workflow intents across stores | Partial code; full conformance pending |
| `async-limits` | Apply concurrency, rate and hard time limits | Partial code; full conformance pending |
| `async-map` | Execute map, starmap and chunks | Partial code; full conformance pending |
| `async-monitor` | Inspect workers, task events and control operations | Partial code; full conformance pending |
| `async-outbox` | Commit business data and reliably dispatch a task | Partial code; full conformance pending |
| `async-publish` | Publish a task with routing and scheduling metadata | Partial code; full conformance pending |
| `async-reclaim` | Recover after worker crash or lease loss | Partial code; full conformance pending |
| `async-register` | Declare tasks and validate worker compatibility | Partial code; full conformance pending |
| `async-results` | Read task results, progress and retention state | Partial code; full conformance pending |
| `async-retry` | Retry transient failures or retain terminal failures | Partial code; full conformance pending |
| `async-signatures` | Compose immutable task signatures | Partial code; full conformance pending |
| `async-states` | Task state machine and durability boundaries | Partial code; full conformance pending |
| `async-task-contract` | Task declaration, dispatch options and wire envelope | Partial code; full conformance pending |
| `async-testing` | Test tasks and workflows deterministically | Partial code; full conformance pending |
| `async-worker` | Claim, execute, persist and acknowledge a task | Partial code; full conformance pending |
| `tools-app` | Generate and register a reusable app | Partial code; full conformance pending |
| `tools-command-catalog` | Central CLI commands and their side effects | Partial code; full conformance pending |
| `tools-commands` | Resolve and run custom management commands | Partial code; full conformance pending |
| `tools-project` | Generate a client project | Partial code; full conformance pending |
| `tools-reload` | Reload the development server safely | Pending implementation or audit |
| `operations-deploy` | Build and deploy web, worker and scheduler roles | Pending implementation or audit |
| `operations-health` | Report startup, readiness and liveness independently | Pending implementation or audit |
| `operations-restore` | Back up and restore data with consistency checks | Pending implementation or audit |
| `operations-scale` | Scale web, workers and storage independently | Pending implementation or audit |
| `operations-telemetry` | Trace requests, queries and tasks without leaking secrets | Pending implementation or audit |
| `testing-contract-matrix` | Required verification before any release claim | Partial code; full conformance pending |
| `testing-suite` | Run unit, integration and generated-client tests | Partial code; full conformance pending |
| `release-modules` | Publish compatible framework modules | Partial code; full conformance pending |
| `map-client-journey` | Developer journey: app, API, Admin and background workflow | Pending implementation or audit |
| `map-client-layout` | Generated client project and daily developer entry points | Partial code; full conformance pending |
| `data-builtin-models` | Built-in PostgreSQL records and relational constraints | Partial code; full conformance pending |
| `connectors-extend` | Add a future connector without changing application APIs | Partial code; full conformance pending |
| `services-catalog` | Shared service methods and provider contracts | Pending implementation or audit |
| `reference-feature-index` | Every feature and its flow | Pending implementation or audit |
| `review-coverage` | Feature coverage and deliberate compatibility boundaries | Pending implementation or audit |

## Cross-cutting release gaps

The complete serializer/API/authentication-account flows, remaining ORM/field/migration catalogs, Admin customization and inline combinations, nested Async canvas/control/reconciliation, shared services and contrib packages, operations, deployment examples, reference documentation, and full conformance matrix still require work. Independently audited production readiness, exhaustive Django/Celery compatibility, package publication and deployment have not been established.
