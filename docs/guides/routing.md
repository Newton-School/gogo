# URLs and routing

Declare URL patterns in each app's `urls.go`, then combine them in the project URL tree. Handlers remain ordinary Go `http.Handler` values.

## Paths, methods and namespaces

Use `urls.Path` for converter-based paths, `urls.RePath` for regular-expression paths, and `urls.Include` for a prefix plus namespace. Supply method names explicitly when a route should be restricted.

{{code docs/examples/routing_test.go}}

This complete test demonstrates an app namespace, a typed URL parameter, reverse routing and a real in-process HTTP request. Run it with the other [documentation examples](testing.md).

## Route vocabulary

| API | Purpose |
| --- | --- |
| `Path(pattern, handler, name, methods...)` | Declare a named route |
| `RePath` | Declare a regular-expression route |
| `Include(prefix, namespace, routes...)` | Compose an app URL tree |
| `New`, `NewWithConverters` | Validate/build the router |
| `Params`, `Param`, `Name` | Read resolved values from a request |
| `Router.Reverse` | Build a URL from a route name, parameter map and query values |
| `Router.Resolve`, `Routes`, `Describe` | Inspect configured routing |

The built-in converter map is available through `Builtins()`. Inspect converter rules when identifiers have special constraints; do not reinterpret an arbitrary path value as an authorized object.

## Keep authorization in handlers and middleware

Only mounting a route on a particular process does not authenticate callers. Scope queries and check permissions at the trusted handler/service boundary. Unknown paths, method mismatches, invalid parameters and reverse-resolution failures are distinct outcomes.

Root fallback integration for redirects or flatpages must be configured explicitly. Do not let a fallback replace an authorization failure or steal a valid application route.
