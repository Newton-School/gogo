# Static assets

Static assets are deliberately public files such as CSS and JavaScript. They are separate from private uploads and should come from explicitly registered sources.

## Collection workflow

Register ordered static sources, an output destination and the collector. Add `management.StaticCommands` to your project when you want CLI collection and lookup.

```sh
go run manage.go findstatic catalog/site.css
go run manage.go collectstatic --dry-run
go run manage.go collectstatic
```

Use a logical filename that actually exists in your sources. Dry-run validates/fingerprints without publishing destination files. Normal collection publishes fingerprinted assets and their manifest according to the collector contract.

## Sources, precedence and manifest

This complete example constructs sources, performs a dry run, and resolves template-facing asset metadata. It does not publish an arbitrary filesystem tree:

{{code examples/showcase/recipes/static/example_test.go}}

Run `go test -v ./recipes/static` from `examples/showcase`. Configure your actual destination and register `management.StaticCommands` before running the collection commands above; they are not automatically available in a fresh project.

Source precedence is explicit and inspectable. Duplicate logical names are resolved through configured ownership/order, not arbitrary directory traversal. The manifest maps logical names to fingerprinted public URLs.

Register the manifest's template tags so templates resolve the collected URLs. Static serving and production CDN/proxy placement remain project/deployment choices; a setting alone does not mount an HTTP server.

## CSS and boundaries

Supported CSS URL rewriting is bounded and validated. It is not full Django staticfiles post-processing parity or a general JavaScript build pipeline. Use an external frontend asset build when your application needs one, then register only its intended public output.

Never add a repository root, `.env`, private upload directory or credential volume as a public static source. The [static recipe](https://github.com/Newton-School/gogo/tree/master/examples/showcase/recipes/static) demonstrates dry-run collection and template integration without publishing files.
