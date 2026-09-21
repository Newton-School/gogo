package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Newton-School/gogo/core/db"
	gogomail "github.com/Newton-School/gogo/core/mail"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type PasswordResetConfig struct {
	Accounts       *Accounts
	Mail           gogomail.Backend
	From, ResetURL string
	// VerifiedRecipient must resolve a current verified delivery target from
	// trusted application state. An identifier is never assumed to be email.
	// Return an error/empty string for ineligible accounts. It must honor context.
	// Changing/revoking that target must advance the account's auth_version,
	// like changing a credential; existing tokens must not outlive that change.
	VerifiedRecipient func(context.Context, Principal) (string, error)
	// The synchronous request path pads normal outcomes to this duration and
	// gives database/mail operations the same deadline. Default 5s, 10ms..30s.
	// Custom backends/callbacks must honor context; arbitrary user code cannot
	// be forcibly interrupted, so this is not a constant-time network guarantee.
	RequestTimeout time.Duration
	Development    bool // permits HTTP reset links only on explicit loopback hosts
	// OnEvent receives a fixed safe outcome code, never identity, token,
	// recipient, provider text or validator errors. Keep it non-blocking.
	OnEvent func(context.Context, string)
}

type PasswordReset struct {
	accounts  *Accounts
	backend   gogomail.Backend
	from      string
	resetURL  url.URL
	recipient func(context.Context, Principal) (string, error)
	timeout   time.Duration
	onEvent   func(context.Context, string)
}

func NewPasswordReset(config PasswordResetConfig) (*PasswordReset, error) {
	if config.Accounts == nil || config.Mail == nil || config.VerifiedRecipient == nil {
		return nil, errors.New("auth: password reset requires accounts, mail and verified recipient resolver")
	}
	want := (&PasswordResetRecord{}).Schema()
	actual, ok := config.Accounts.store.Registry.Get(want.Key())
	wanted, e1 := want.Fingerprint()
	got, e2 := actual.Fingerprint()
	if !ok || e1 != nil || e2 != nil || wanted != got {
		return nil, errors.New("auth: matching password reset model must be registered")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 5 * time.Second
	}
	if config.RequestTimeout < 10*time.Millisecond || config.RequestTimeout > 30*time.Second {
		return nil, errors.New("auth: invalid reset request deadline")
	}
	if sender, err := mail.ParseAddress(config.From); err != nil || sender.Address == "" || strings.ContainsAny(config.From, "\r\n") {
		return nil, errors.New("auth: valid reset sender required")
	}
	link, err := url.Parse(config.ResetURL)
	if err != nil || link.Host == "" || link.User != nil || link.RawQuery != "" || link.Fragment != "" || link.Opaque != "" {
		return nil, errors.New("auth: absolute reset URL without credentials, query or fragment required")
	}
	loopback := link.Hostname() == "localhost"
	if ip, err := netip.ParseAddr(link.Hostname()); err == nil {
		loopback = ip.IsLoopback()
	}
	if link.Scheme != "https" && !(config.Development && loopback && link.Scheme == "http") {
		return nil, errors.New("auth: HTTPS reset URL required")
	}
	return &PasswordReset{accounts: config.Accounts, backend: config.Mail, from: config.From, resetURL: *link, recipient: config.VerifiedRecipient, timeout: config.RequestTimeout, onEvent: config.OnEvent}, nil
}

// Request returns the same acknowledgement for absent/inactive/unusable users,
// failed lookup/storage and failed/unknown/simulated mail delivery. Only caller
// cancellation is returned. HTTP callers must apply IP/identity throttling and
// CSRF before invoking it. No token is returned, logged or queued as plain JSON.
// This is bounded synchronous delivery after confirmed token commit; encrypted
// durable delivery is a separate port/workflow, not simulated by goroutines.
func (p *PasswordReset) Request(ctx context.Context, identifier string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(p.timeout)
	work, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	code := p.safeRequest(work, identifier)
	// Pad ineligible and unavailable paths too. A canceled caller is not forced
	// to wait and cannot cause a background goroutine to continue sending mail.
	if remaining := time.Until(deadline); remaining > 0 {
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	p.observe(ctx, code)
	return ctx.Err()
}

func (p *PasswordReset) safeRequest(ctx context.Context, identifier string) (code string) {
	code = "unavailable"
	// A provider or application resolver panic must not turn only an eligible
	// identity into a distinctive HTTP failure. Atomic owns rollback on panic;
	// no raw panic value is observed, logged or returned across this boundary.
	defer func() { _ = recover() }()
	return p.request(ctx, identifier)
}

func (p *PasswordReset) request(ctx context.Context, identifier string) string {
	if db.InTransaction(ctx, p.accounts.store.Backend.Alias()) {
		return "storage_unavailable"
	}
	if len(identifier) > 512 {
		return "acknowledged"
	}
	normalized, err := p.accounts.identifier(identifier)
	if err != nil {
		return "acknowledged"
	}
	user, principal, err := p.accounts.snapshot(ctx, orm.Q("identifier", normalized))
	if errors.Is(err, ErrUnauthenticated) {
		return "acknowledged"
	}
	if err != nil {
		return "lookup_unavailable"
	}
	if !user.Active || !HasUsablePassword(storedHash(user)) {
		return "acknowledged"
	}
	recipient, err := p.recipient(ctx, principal)
	if err != nil || recipient == "" {
		return "acknowledged"
	}
	if parsed, err := mail.ParseAddress(recipient); err != nil || parsed.Address == "" || strings.ContainsAny(recipient, "\r\n") {
		return "recipient_unavailable"
	}
	id, bearer, digest, err := newResetToken()
	if err != nil {
		return "storage_unavailable"
	}
	issued := time.Now().UTC().Truncate(time.Microsecond)
	record := &PasswordResetRecord{ID: id, UserID: user.ID, SecretDigest: digest, AuthVersion: user.AuthVersion, CreatedAt: issued, ExpiresAt: issued.Add(time.Hour)}
	err = db.Atomic(ctx, p.accounts.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		// Lock before issuing; a recipient callback cannot cause a stale account
		// version to receive a redeemable token. Confirmation always locks token
		// then account; issuance holds no existing token locks.
		current, err := orm.For(p.accounts.store, p.accounts.models.User).Filter(orm.Q("id", user.ID)).SelectForUpdate(false, false).Get(ctx)
		if err != nil {
			return err
		}
		if !current.Active || current.AuthVersion != user.AuthVersion || storedHash(current) != storedHash(user) {
			return ErrAccountChanged
		}
		return p.saveReset(ctx, record, true, user.AuthVersion, storedHash(user))
	})
	if err != nil {
		return "storage_unavailable"
	} // including unknown commit: never send
	link := p.resetURL
	link.Fragment = bearer
	message := gogomail.Message{From: p.from, To: []string{recipient}, Subject: "Reset your password", Text: "A password reset was requested for your account. Open this link within one hour to choose a new password:\n\n" + link.String() + "\n\nIf you did not request this, you can ignore this message.", Sensitive: true}
	receipt, sendErr := p.backend.Send(ctx, message)
	if sendErr == nil && receipt.AllAccepted() {
		return "accepted"
	}
	if sendErr == nil && receipt.Simulated {
		return "simulated"
	}
	for _, recipient := range receipt.Recipients {
		if recipient.State == gogomail.Unknown {
			return "delivery_unknown"
		}
	}
	if errors.Is(sendErr, gogomail.ErrUnknown) {
		return "delivery_unknown"
	}
	return "delivery_failed"
}

func (p *PasswordReset) observe(ctx context.Context, code string) {
	if p.onEvent != nil {
		defer func() { _ = recover() }()
		p.onEvent(ctx, code)
	}
}

// Confirm consumes the token and changes the password in one owned transaction.
// Token row then user row are locked in stable order. Invalid tokens and policy
// rejection do not consume it. The account policy must explicitly permit
// "reset_password" after secret proof; staff status alone is never authority.
// No authenticated principal is returned, even after a successful reset.
func (p *PasswordReset) Confirm(ctx context.Context, bearer, password string) (PasswordChangeResult, error) {
	result := PasswordChangeResult{State: PasswordUnchanged}
	id, digest, err := parseResetToken(bearer)
	if err != nil {
		return result, ErrResetToken
	}
	if password == "" || len(password) > 4096 {
		return result, ErrPasswordValidation
	}
	if db.InTransaction(ctx, p.accounts.store.Backend.Alias()) {
		return result, errors.New("auth: reset confirmation requires an owned commit boundary")
	}
	err = db.Atomic(ctx, p.accounts.store.Backend, db.AtomicOptions{}, func(ctx context.Context) error {
		token, err := orm.For(p.accounts.store, func() *PasswordResetRecord { return &PasswordResetRecord{} }).Filter(orm.Q("id", id)).SelectForUpdate(false, false).Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return ErrResetToken
		}
		if err != nil {
			return err
		}
		if !resetDigestEqual(token.SecretDigest, digest) || token.UsedAt != nil || !token.ExpiresAt.After(time.Now()) {
			return ErrResetToken
		}
		user, err := orm.For(p.accounts.store, p.accounts.models.User).Filter(orm.Q("id", token.UserID)).SelectForUpdate(false, false).Get(ctx)
		if errors.Is(err, orm.ErrNotFound) {
			return ErrResetToken
		}
		if err != nil {
			return err
		}
		if !user.Active || user.AuthVersion != token.AuthVersion || !HasUsablePassword(storedHash(user)) {
			return ErrResetToken
		}
		previous := storedHash(user)
		if err := p.accounts.permit(ctx, AccountChange{Action: "reset_password", UserID: user.ID}); err != nil {
			return err
		}
		// Re-read after policy callbacks before deriving password-validator claims.
		_, principal, err := p.accounts.snapshot(ctx, orm.Q("id", user.ID))
		if err != nil {
			return err
		}
		if !principal.Active || int64(principal.AuthVersion) != token.AuthVersion {
			return ErrResetToken
		}
		hash, err := p.accounts.hash(ctx, password, principal)
		if err != nil {
			return err
		}
		if err := bumpVersion(user); err != nil {
			return err
		}
		user.PasswordHash = &hash
		if err := p.accounts.saveUser(ctx, user, orm.SaveOptions{UpdateFields: []string{"password_hash", "auth_version", "updated_at"}, Guard: func(ctx context.Context, _ models.Record) error {
			current, err := orm.For(p.accounts.store, p.accounts.models.User).Filter(orm.Q("id", user.ID)).Get(ctx)
			if err != nil {
				return err
			}
			if !current.Active || current.AuthVersion != token.AuthVersion || storedHash(current) != previous {
				return ErrResetToken
			}
			return p.resetStillValid(ctx, token)
		}}); err != nil {
			return err
		}
		used := time.Now().UTC().Truncate(time.Microsecond)
		token.UsedAt = &used
		return p.saveReset(ctx, token, false, user.AuthVersion, hash)
	})
	if err != nil {
		var committed *db.CommittedCallbackError
		if errors.As(err, &committed) {
			result.State = PasswordChanged
		} else if db.IsCode(err, db.UnknownCommit) {
			result.State = PasswordChangeUnknown
		}
		return result, err
	}
	result.State = PasswordChanged
	return result, nil
}

func (p *PasswordReset) resetStillValid(ctx context.Context, want *PasswordResetRecord) error {
	current, err := orm.For(p.accounts.store, func() *PasswordResetRecord { return &PasswordResetRecord{} }).Filter(orm.Q("id", want.ID)).Get(ctx)
	if err != nil {
		return err
	}
	if current.UserID != want.UserID || current.AuthVersion != want.AuthVersion || !resetDigestEqual(current.SecretDigest, want.SecretDigest) || current.UsedAt != nil || !current.ExpiresAt.Equal(want.ExpiresAt) || !current.ExpiresAt.After(time.Now()) {
		return ErrResetToken
	}
	return nil
}

func (p *PasswordReset) saveReset(ctx context.Context, record *PasswordResetRecord, insert bool, userVersion int64, userHash string) error {
	expected := *record
	if expected.UsedAt != nil {
		used := *expected.UsedAt
		expected.UsedAt = &used
	}
	options := orm.SaveOptions{ForceInsert: insert}
	if !insert {
		options.UpdateFields = []string{"used_at"}
	}
	options.Guard = func(ctx context.Context, _ models.Record) error {
		if record.ID != expected.ID || record.UserID != expected.UserID || record.SecretDigest != expected.SecretDigest || record.AuthVersion != expected.AuthVersion || !record.CreatedAt.Equal(expected.CreatedAt) || !record.ExpiresAt.Equal(expected.ExpiresAt) || !sameResetTime(record.UsedAt, expected.UsedAt) {
			return ErrResetToken
		}
		user, err := orm.For(p.accounts.store, p.accounts.models.User).Filter(orm.Q("id", expected.UserID)).Get(ctx)
		if err != nil {
			return err
		}
		if !user.Active || user.AuthVersion != userVersion || storedHash(user) != userHash {
			return ErrResetToken
		}
		if !insert {
			return p.resetStillValid(ctx, &expected)
		}
		return nil
	}
	if err := p.accounts.store.Save(ctx, record, options); err != nil {
		return err
	}
	stored, err := orm.For(p.accounts.store, func() *PasswordResetRecord { return &PasswordResetRecord{} }).Filter(orm.Q("id", expected.ID)).Get(ctx)
	if err != nil {
		return err
	}
	if stored.UserID != expected.UserID || stored.AuthVersion != expected.AuthVersion || !resetDigestEqual(stored.SecretDigest, expected.SecretDigest) || !sameResetTime(stored.UsedAt, expected.UsedAt) || !stored.ExpiresAt.Equal(expected.ExpiresAt) || !stored.CreatedAt.Equal(expected.CreatedAt) {
		return ErrResetToken
	}
	user, err := orm.For(p.accounts.store, p.accounts.models.User).Filter(orm.Q("id", expected.UserID)).Get(ctx)
	if err != nil {
		return err
	}
	if !user.Active || user.AuthVersion != userVersion || storedHash(user) != userHash {
		return ErrResetToken
	}
	return nil
}

func sameResetTime(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

func (PasswordReset) String() string                   { return "auth.PasswordReset{configuration:redacted}" }
func (p PasswordReset) GoString() string               { return p.String() }
func (p PasswordReset) Format(state fmt.State, _ rune) { _, _ = fmt.Fprint(state, p.String()) }
