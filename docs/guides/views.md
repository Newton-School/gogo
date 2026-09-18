# Views, responses and WebSockets

Gogo builds on `net/http`. Use an ordinary handler when it fits, or `ghttp.Adapt` to return a structured response and an error. Your views own request validation, authorization and service calls.

## A small JSON view

{{code docs/snippets/catalog/urls.go}}

The response is generated only after the handler returns. Do not expose raw database/provider errors to the client. Propagate the request context into all downstream work.

## Available building blocks

| Feature | Use it for |
| --- | --- |
| Structured responses and adapters | JSON/text/HTML response construction and stable error boundaries |
| Generic template and redirect views | Explicit templates, context and safe redirect targets |
| Generic list/detail model views | Scoped database-backed HTML pages |
| Generic create/update views | Explicit model forms, validation, authorization and save policies |
| Date archive views | Date-indexed model browsing with timezone-aware boundaries |
| Conditional responses | Cache validators and conditional request behavior |
| Streaming | Explicit stream ownership, completion and cancellation |
| WebSockets | Typed message schemas, origin checks, authorization and owned connection lifecycle |

Each generic view has a technical guide under this feature and exact options in [core/http](api-core-http.md). “Generic” does not mean automatic tenant scope or unrestricted model access.

## WebSockets

Register allowed message types and limits before accepting traffic. Check both origin and caller authorization. Keep message work context-aware and bounded; connection success does not authorize every future message.

The WebSocket package exposes an explicit shutdown contract. Stop admitting connections, drain/close existing sessions and only then release resources they use. Ordinary HTTP shutdown alone is not proof that upgraded connections are finished.

See the WebSocket recipe for typed wiring. Its compile-only example does not claim a browser/socket integration run.

## Async versus goroutines

Use goroutines for process-local concurrency you can cancel, bound and join. Use [Async](async.md) when work must survive an HTTP request or process restart. A detached goroutine is not a durable queue.
