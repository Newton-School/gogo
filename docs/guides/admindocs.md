# Staff documentation

`admin/admindocs` exposes documentation assembled from explicitly supplied framework metadata. It is an optional staff surface, not the public documentation site you are reading now.

## What it describes

The following complete, compiled registration sketch shows the constructor and route mounting. It receives an already configured site, router, and template engine; it does not create a permissive staff principal:

{{code admin/admindocs/example_test.go}}

Replace the example's `shop.Product`, field selection, and `shop:product` route with **exact registered names** from your app. For the tutorial Product, the field is `name` (lowercase), not the sketch's `Name`. Mount the handler inside the same account/session middleware as Admin. With the default model policy, explicitly provision `admindocs.view_documentation` for the staff who should read it; staff status alone is not enough.

Run `go test ./admin/admindocs/...` from the contributor checkout to run the package's permission and rendering tests. The registration sketch is compile-checked, not a complete standalone web application. Start with [Admin wiring](admin-wiring.md) for the surrounding services.

- Registered model and field metadata.
- Declared URL/view information.
- Template tags and filters exposed by description APIs.
- The application-owned documentation registry and its configured bounds.

Construct its configuration with the relevant registry/router/template metadata and mount its handler under staff authorization. Do not enumerate arbitrary filesystem paths or execute application callbacks merely to display documentation.

## Access and disclosure

An internal route is still a route: require current staff authority and preserve model/field visibility. Metadata can reveal private implementation structure even when no model rows are returned. Do not include secrets, provider URLs, private paths or tenant data in labels/help text.

Use the technical staff-docs guide for exact configuration and refusal rules and [admin/admindocs](api-admin-admindocs.md) for its public constructor types.
