# Staff documentation

`admin/admindocs` exposes documentation assembled from explicitly supplied framework metadata. It is an optional staff surface, not the public documentation site you are reading now.

## What it describes

- Registered model and field metadata.
- Declared URL/view information.
- Template tags and filters exposed by description APIs.
- The application-owned documentation registry and its configured bounds.

Construct its configuration with the relevant registry/router/template metadata and mount its handler under staff authorization. Do not enumerate arbitrary filesystem paths or execute application callbacks merely to display documentation.

## Access and disclosure

An internal route is still a route: require current staff authority and preserve model/field visibility. Metadata can reveal private implementation structure even when no model rows are returned. Do not include secrets, provider URLs, private paths or tenant data in labels/help text.

Use the technical staff-docs guide for exact configuration and refusal rules and [admin/admindocs](api-admin-admindocs.md) for its public constructor types.
