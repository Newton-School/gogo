# CSRF, signing and HTTP security

## Sign and verify a purpose-bound value

This complete test shows the signing API, expiry bound, and tamper rejection. Run `go test ./docs/examples -run Example_signing -v` from the framework checkout:

{{code docs/examples/security_test.go}}

Use a persistent private key from client settings in a real application. Generating a new key on every request/restart invalidates earlier values. A signature proves integrity, not authorization or confidentiality; still scope the referenced object for the current caller.

Gogo supplies explicit security primitives. Your project must configure and mount them around the routes that need them.

## Host and proxy validation

`security.Headers` validates allowed hosts and handles configured security headers. Trusted proxies are CIDR networks, not arbitrary hostnames. Forwarded scheme/client IP must be trusted only when the immediate peer belongs to that allowlist.

Use `IsSecure` and `ClientIP` through the configured trusted boundary rather than reading untrusted forwarding headers directly. Configure HTTPS and the intended content-security policy for production. The generated project wires basic allowed-host handling; adding a setting alone does not install every security component.

## CSRF

Create `security.CSRF` middleware with explicit cookie/origin settings and place it on cookie-authenticated state-changing routes. Render the token into forms or send it through the documented request header. Use `CSRFToken` and the rotation helpers instead of inventing token handling.

Test missing, mismatched, cross-origin and multipart submissions. Exempting an endpoint requires another appropriate authentication/integrity mechanism; it is not a shortcut for a broken form.

## Signed values

`NewSigner` takes a current key, optional fallback keys and a purpose. Purpose separation prevents a value signed for one use from being accepted as another. Configure expiry and key rotation deliberately.

Use `RandomToken` for cryptographic random tokens. Never copy a test key into a real application. Signing protects integrity, not secrecy; URLs, cookies and signed cursors can still be read by their recipients.

## Redirects and output

Use `SafeNext` for local post-login destinations. Escape user-visible text and preserve contextual template escaping. Do not construct raw SQL, untrusted HTML, arbitrary file paths or template names directly from request values.

## Authorization checklist

- Scope data before queries, counts, searches, exports and related-object lookup.
- Recheck permission at the command/job/service boundary, not only in the UI.
- Separate public API fields from model and Admin declarations.
- Redact tokens, passwords, provider errors, query parameters and task payloads where appropriate.
- Bound request sizes, upload bytes/pixels, pagination, callback work and queue concurrency.
- Treat cancellation or a missing write response as a possibly uncertain outcome, not automatic rollback.

These are application obligations supported by framework contracts. Passing unit tests or a dependency scan is not a claim that a deployment is free of security defects.
