# Optional Admin documentation

`admindocs.New(site, options)` builds a read-only developer reference from
explicit model/field selections, a router's declared routes, explicitly supplied
view prose, and a template engine's effective tag/filter names. It does not query
model records, execute documented code, inspect handler identities, browse source
files, or enumerate a filesystem.

Register the Site's models before calling `New`. A successful construction freezes
the Site's registrations, just like `Site.Handler`. Explicitly mount the returned
handler at the configured `admin.Config.Prefix + "doc/"` inside the same `sessions.Middleware` and
`auth.SessionMiddleware` used by the Admin site. The default path is `/admin/doc/`;
a root-prefix Site uses `/doc/`. Wrong mounts and noncanonical variants return 404.
Only GET and HEAD are supported. No import or application initializer installs a
route; production can disable documentation by omitting the mount entirely.

## Authorization

Every request requires a verified, active staff principal and the existing Site
policy's `view` action on `{App: "admindocs", Model: "documentation", ID: siteName}`.
With `auth.ModelPolicy`, the grant is `admindocs.view_documentation`, exposed as
`admin.DocumentationPermission`. Staff status alone is insufficient. Custom Site
policies remain supported and retain token-scope ceilings.

Applications provision this permission explicitly through their authorization
system. Installing documentation creates no model, content type, permission or
migration. Each selected model also requires the Site's existing `view` policy and
its additional `ModelAdmin.Authorize` hook. An initial clean model denial omits
that model; operational failures fail the whole request closed. Documentation
permission is not an object-data or mutation grant.

The session middleware checks current account state and `auth_version` at request
entry. Grants are rechecked after label/template callbacks before page bytes are
written. A plain `ModelPolicy` reads the verified request principal; applications
requiring a fresh mid-request account lookup must implement that in their policy.
No atomic snapshot across multiple permission checks is promised. Policy, label
and loader callbacks must be read-only and concurrency-safe.

Anonymous, inactive, nonstaff and denied documentation requests return 403 without
login redirects or reference metadata. Failures and panics return a generic 503.
Responses use the existing Admin security headers, `private, no-store` and
`Vary: Cookie`. Conditional requests still authorize and render; there is no cached
304 shortcut. HEAD follows the same grants without emitting a body. Transfer
failure aborts the HTTP response without retrying or appending another response.

## Explicit descriptors

- `Models` selects **registered** model keys and a nonempty field allowlist.
  Fields display only name, kind, null/blank flags, effective editability and
  primary-key membership. Descriptions are explicit plain strings from options.
  Defaults, database defaults/SQL, schema comments, implicit help text, choices,
  codecs, validators, factories and stored values are never projected.
- `Router` uses `urls.Router.Describe`; namespaced names, declared patterns and
  declared method lists are retained in route order. An empty method list means
  the route is not method-restricted. No handler or converter is invoked or
  retained in the reference. Request metadata and query values are not included.
- `Views` contains explicit titles/descriptions keyed to an existing named route.
  It is application-supplied documentation, not inferred from Go function names,
  comments, reflection or runtime source lookup.
- `Templates` uses `Engine.Describe` without loaders, processors, tags or filters
  running. It describes the supplied engine, which need not be the Site's private
  rendering engine. Builtin lexical, structural and rendering tags are included;
  custom names are included only where they can actually dispatch. Closing and
  branch delimiters are syntax of the corresponding opening tag. Filter overrides
  retain the effective name once, without claiming the builtin implementation.
  `Tags`/`Filters` optionally describe existing names; unknown names are rejected.

Options and descriptors are detached at construction. There is no automatic
full-model selection, `models.Registry.All` traversal, dynamic renderer callback,
custom page interface or template-path parameter. The fixed embedded
`documentation.html` extends the existing Admin `base.html`; explicit project
template loaders keep their ordinary override behavior. Text is contextually
escaped, with reference section navigation, table captions and row/column headers.

## Bounds and verification

Construction rejects partial/over-budget inventories: at most 256 selected models,
256 selected fields per model and 8,192 total selected fields; 4,096 inspected
declarations per selected model and 65,536 total; 4,096 routes and views; 16,384
declared route-method entries; and 512 names each for tags and filters. All retained
text and inspected declaration names share a 1 MiB budget, with individual prose
capped at 4,096 UTF-8 bytes.
The template engine retains its own rendering/output limits. Requests ignore body
handles with an explicit zero length, without reading or closing them; nonzero or
unknown lengths and transfer-encoded bodies are rejected.

The example is a compiled registration sketch, not session/backend setup. Unit and
integration tests separately exercise real request and account/session boundaries.
No browser or visual-accessibility verification is implied by source/HTTP tests.
