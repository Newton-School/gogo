# Troubleshooting

Start with the exact command, selected resource and safe error category. Do not paste credentials or complete task/request payloads into an issue.

## Common problems

| Symptom | Check |
| --- | --- |
| `gogo` is not found | Ensure the directory used by `go install` is on PATH |
| Wrong API after installation | Pin `v1.0.0-alpha.2`; the older stable line can be selected by `@latest` |
| Missing required setting | Set it in your client project's environment; confirm which resource the command selects |
| Unknown `GOGO_*` key | Fix the spelling or explicitly declare the custom setting in the project schema |
| Unexpected config under standalone `gogo` | Run `go run manage.go ...` inside the configured application |
| Model missing from registry | Register the app and run `generate`; inspect generated descriptors |
| Migration drift | Run `generate --check` and `makemigrations APP --check`; do not edit applied history |
| Admin does not appear | Install/wire Admin, register models, mount its handler and configure accounts/session policy |
| `worker` or `beat` is unknown | Register the corresponding optional Async factory; installation alone is not activation |
| Accepted task never completes | Check a worker consumes the selected queue/version and the required relays/dispatcher are running |
| Redis connection is refused | Verify URL/database, role, version, TLS/authentication and durability policy |
| Development Redis rejects a container hostname | Development requires loopback; use the documented local showcase topology or a correctly secured deployment configuration |
| API returns 404 for an existing record | Check current row scope and identifier decoding; missing and out-of-scope details are intentionally indistinguishable |
| API refuses a query parameter | Use only configured filters, ordering and one pagination mode |
| Form relation choice fails | The resolver must authorize each submitted ID in the current scope |
| Save accepted invalid input | Direct ORM `Save` does not implicitly call `FullClean`; enforce validation in the write path |
| Local file has no public URL | Use the authorized file service/download handler; direct local URLs are unsupported |
| Health endpoint is live but not ready | Check startup/admission state and only that role's selected dependencies |
| Timed-out operation later appears successful | Cancellation or a lost response does not necessarily undo an accepted external write |

## Safe diagnosis

Use `go run manage.go help` for installed commands and `diffsettings` for redacted resolved settings. Review errors at the owning feature boundary; do not infer a provider outage is invalid user input or a missing row.

For unknown write outcomes, retain the original operation identity, receipt, cursor or envelope. Follow the feature's reconciliation contract instead of blindly generating another request.

## Report an issue

Include the exact module versions, Go version, operating system, selected connector and a minimal reproduction. State whether the failure was observed in a unit test, real database/Redis test, browser or deployment. Remove tokens, private URLs, database passwords, personal records and machine-specific paths.
