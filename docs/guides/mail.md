# Email

`core/mail` builds bounded messages and delivers them through an explicit backend. Message preparation, provider acceptance and final delivery to a mailbox are different events.

## Compose and send

Use `mail.Message` for sender, recipients, subject, content, alternatives and attachments. Validate with the package's limits and send through `SendMail`, `SendMassMail` or a configured `Service`.

Do not concatenate untrusted header lines. Header validation rejects injection; Bcc recipients belong in the SMTP envelope rather than visible message headers. Keep attachment count, bytes, recipient count and total message size bounded.

## Backends

| Backend | Use |
| --- | --- |
| SMTP | Real delivery handoff with explicit TLS/authentication and bounded transport |
| Memory | Bounded process-local test outbox |
| Console | Explicit development output |
| File | Explicit private development outbox |
| Dummy | Deliberate simulated acceptance without external delivery |

Development backends are not automatic production fallbacks. Sensitive messages have stricter output rules; do not send reset links or credentials to ordinary console/file logs.

## SMTP setup

Construct `NewSMTP` with verified transport settings, credentials at the trusted boundary, timeouts and pool limits. The Core environment vocabulary includes SMTP host/port/username/password and sender, but your project must wire these values into the actual backend.

Inspect each recipient receipt and the send error. Partial acceptance is possible. A lost reply after SMTP DATA does not prove rejection and must not trigger an unconditional resend.

## Async delivery

If you queue mail, define an application task and an idempotency/delivery policy. Ordinary task payloads must not contain account-reset secrets. Core's password reset has its own sensitive synchronous path; encrypted durable reset delivery is not included in this release.

The [services recipe](https://github.com/Newton-School/gogo/tree/master/examples/showcase/recipes/services) covers multipart composition, Bcc privacy, header denial and simulated outbox behavior without sending actual mail.
