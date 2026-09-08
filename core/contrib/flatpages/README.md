# Site-bound flat pages

`flatpages` stores shared pages and explicit site memberships, provides one
authorized transactional save boundary, and serves named application templates.
Importing it never migrates, seeds data, installs routes or registers an Admin.

## Register and wire

Register `sites.Site`, `flatpages.FlatPage` and `flatpages.FlatPageSite` schemas in
the application's model registry. Add `sites.Migrations()` followed by
`flatpages.Migrations()` to its migration executor. The flatpages migration has
an explicit dependency on the initial sites migration.

```go
pages, err := flatpages.New(flatpages.Config{
    Backend: backend,
    AuthorizeChange: authorizePageChange,
})
if err != nil { return err }

handler, err := flatpages.NewHandler(pages, flatpages.HandlerOptions{
    Sites: siteResolver,
    Authorize: authorizePageReader,
    Render: flatpages.RenderOptions{
        Templates: templates.Config{Loaders: []templates.Loader{
            templates.MapLoader{
                "flatpages/default.html": "<h1>{{ flatpage.title }}</h1><main>{{ flatpage.content }}</main>",
            },
        }},
    },
})
if err != nil { return err }
router, err = router.WithNotFound(handler)
```

`backend`, `siteResolver`, both authorization functions and `router` above are
application dependencies. Configure the store and site resolver against the
same logical site database. Install verified authentication and appropriate
host/security middleware outside the handler. Site selection is not tenant
authorization; token scope ceilings and current account/site/page grants remain
the policy owner's responsibility.

The handler may also be attached to an explicit route. `WithNotFound` handles
only the router's final miss, never a matched view's own 404. There is no global
response interception or implicit trailing-slash retry.

## Save a page and its complete site set

```go
saved, err := pages.SaveDomain(ctx, flatpages.SaveInput{
    Page: flatpages.Draft{
        URL: "/about/", Title: "About", Content: "Welcome",
    },
    SiteIDs: []string{currentSiteID},
})
if err != nil { return err }

saved, err = pages.SaveDomain(ctx, flatpages.SaveInput{
    Page: flatpages.Draft{
        ID: saved.Page.ID, URL: "/company/", Title: "Company",
        Content: "Members only", RegistrationRequired: true,
    },
    SiteIDs: []string{currentSiteID, secondSiteID},
})
```

The site list is a complete replacement. Empty detaches every site; duplicates
are normalized away and output is sorted. Empty page ID creates a UUID;
nonempty ID updates an existing page only and never silently inserts.

For identity-based recovery, generate and durably record a nonzero UUID in the
application first, then supply it as `Page.ID` with `Create: true`. This is
create-only: an existing ID conflicts and is never overwritten. After an
uncertain commit, use an explicitly authorized ORM query to reconcile that ID
and its memberships. The package does not provide an unscoped public reader.
Internally allocated IDs are not returned on failure; creation without a
caller-known ID, especially with no memberships, needs operator reconciliation
and must not be blindly retried.

| Stage | Behavior |
|---|---|
| Input | Copy configuration and input before callbacks; validate bounded canonical fields and UUIDs |
| Transaction | Own durable transaction; reject an ambient transaction on this backend alias |
| Read | Lock the existing page, its bounded memberships and the sorted union of affected sites |
| Grants | `CreatePage` or `UpdatePage`, then `AttachSite`, `RetainSite` or `DetachSite` for every affected site |
| Before write | Re-read exact page, membership and site snapshots after all policy callbacks |
| Write | Save the page; remove, create or update memberships and their reserved URL together |
| Before commit | Verify the exact persisted page, complete memberships and site snapshots |
| Result | Return detached `Saved` only after a confirmed successful commit |

`Change.Before` is nil only for creation; each call receives a fresh copy.
Page actions have an empty `SiteID`. A retained site needs authorization because
shared content changes affect it too. Callbacks are read-only and must lock any
external ACL state needed for their guarantees; supplied page/site metadata is
not current authority evidence. A callback error or panic aborts the operation.
Exact `ErrForbidden` is a denial; mixed/wrapped operational failures are not.

The database enforces page/site foreign keys, unique `(site_id, url)` and unique
`(page_id, site_id)`. Equality between the page URL and each membership URL is a
`SaveDomain` invariant, **not** a composite database constraint. Ordinary ORM
saves, direct/bulk SQL and generic many-to-many writes are privileged escape
hatches and do not maintain it. Existing drift fails closed; it is not repaired
silently. Use this domain operation for application editors, including a future
Admin adapter. There is no automatic deletion, Admin wiring or cache invalidation.

Only a pure recognized mapping uniqueness conflict becomes `ErrConflict`.
An existing page identity observed before writing also conflicts. A concurrent
create collision may instead be `ErrUnavailable` when the connector's implicit
primary-key constraint name is not part of the portable contract; it never
updates the winning page.
Pre-commit failures return no saved page. Uncertain commit acknowledgement or a
commit panic returns `ErrOutcomeUnknown`: reconcile storage before any retry;
neither failure nor rollback has been proved. A confirmed successful commit
remains success if cancellation arrives afterward. This is not an idempotency
receipt or an automatic retry protocol.

## Read and render

`Lookup(ctx, LookupInput{SiteID: siteID, URL: "/about/"})` uses its own read-only
repeatable-read transaction. It verifies an active site, at most one matching
membership, and the page's exact canonical URL. Duplicate, missing referenced,
malformed, drifted, failed or incompletely closed rows never return partial
content. A normal absent membership returns `found == false`. Lookup is a
server-side data read, not an authorization grant or safe-HTML assertion.

| HTTP branch | Outcome |
|---|---|
| GET or HEAD | Select exact canonical path; query parameters do not select a page |
| Other method | 405 with `Allow: GET, HEAD`; no data read |
| Request body or malformed path/host | 400; no body parsing |
| Missing/inactive site or absent membership | Configured `NotFound`, otherwise ordinary 404 |
| Registration-required without a verified active authenticated identity | Contentless 404 before authorization/render callbacks |
| Reader grant | Mandatory before rendering and again immediately before response |
| Exact policy/sanitizer `ErrForbidden` | Contentless 404 |
| Provider, template, mixed policy failure, panic or cancellation | Contentless 503; never a different site's page |
| Successful render | 200 HTML, `private, no-store`, `nosniff`, exact content length |
| HEAD | Same checks and content length, no response body |

The read grant receives detached selection snapshots. Revalidate current
authority by stable identity, not cached site labels or content fields. Custom
404 handlers own their response policy. Response-write failures abort the
connection rather than pretending a partial HTML body was successfully sent.

Stored content is always template **data**, never template source. The private
template engine receives lowercase `flatpage` fields (`id`, `url`, `title`,
`content`, `template_name`, `registration_required`) and `site` fields (`id`,
`domain`, `display_name`, `active`). These reserved values override context
processors. Ordinary content is escaped under the engine's default autoescaping.

The default template name is `flatpages/default.html`; applications must supply
it. A configured custom name must be in `AllowedTemplates`, which defaults to
only the default name. Missing or disallowed templates do not silently fall
back. Partial selectors and traversal paths are rejected. Startup collections
and built-in map loaders are copied; arbitrary loader/filter/tag services are
trusted application code with their own concurrency responsibilities.

`Render.Sanitize` is an optional trusted function invoked only after reader
authorization. It returns `templates.SafeHTML` after applying the application's
HTML policy. Casting a string to `SafeHTML` does not sanitize it. Trusted template
authors, autoescape overrides and extension filters can change escaping; this is
not a sandbox for hostile templates. There is no bundled HTML sanitizer.

## Bounds and compatibility

Paths are root-relative ASCII-escaped keys: decode unreserved escapes, uppercase
remaining escapes and encode Unicode. Case, internal repeated slashes and the
final slash remain significant. Query/fragment text is forbidden in stored URLs;
HTTP queries are ignored. Authority-like paths, controls, backslashes, dot
segments, encoded separators and recursive encoded structure are rejected.

| Limit | Default / maximum |
|---|---|
| Site IDs supplied per save | 128 / 1,024, before deduplication |
| Raw and canonical path | 2,048 bytes |
| Title | Required, 255 Unicode code points |
| Content and sanitizer result | 1 MiB, valid UTF-8 without NUL |
| Template name | 255 bytes; 64 allowlisted names |
| Rendered output | 8 MiB / 16 MiB |
| HTTP operation timeout | 5 seconds; configurable 1 ms–1 minute |

Attaching a page to an active site makes it selectable; there is no separate
draft/publication flag. Inactive sites may retain memberships but cannot serve
pages. The package has no page cache, login redirect, comments integration,
automatic Admin form, response-wide 404 middleware or full Django conformance
claim. Those integrations remain explicit work in the approved AgentFlow plan.
