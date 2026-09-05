package integration

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/gogo-integration/internal/testservice"
	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/contrib/contenttypes"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/mail"
	"github.com/Newton-School/gogo/core/migrations"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

// This explicit test mail sink exposes messages only inside the fixture. No
// external recipient/provider is contacted and no token is written to logs.
type resetMailbox struct {
	mu       sync.Mutex
	messages []mail.Message
	err      error
	onSend   func(mail.Message)
}

func (m *resetMailbox) Send(_ context.Context, message mail.Message) (mail.Receipt, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.onSend != nil {
		m.onSend(message)
	}
	m.messages = append(m.messages, message.Clone())
	return mail.Receipt{Backend: "fixture", Simulated: true}, m.err
}
func (m *resetMailbox) bearer(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.messages) == 0 {
		t.Fatal("expected a reset message")
	}
	message := m.messages[len(m.messages)-1]
	if !message.Sensitive || len(message.To) != 1 || message.To[0] != "verified@example.test" {
		t.Fatal("reset delivery boundary")
	}
	for _, line := range strings.Split(message.Text, "\n") {
		if strings.HasPrefix(line, "https://example.test/reset#") {
			link, err := url.Parse(line)
			if err != nil {
				t.Fatal("invalid reset link")
			}
			if link.RawQuery != "" || len(link.Fragment) != 80 {
				t.Fatal("secret not confined to URL fragment")
			}
			return link.Fragment
		}
	}
	t.Fatal("missing reset link")
	return ""
}

func TestPostgresPasswordResetSingleUseAndCredentialRaces(t *testing.T) {
	ctx := context.Background()
	backend := testservice.Postgres(t)
	registry := &models.Registry{}
	for _, schema := range append(append(auth.Schemas(), auth.PasswordResetSchemas()...), (&contenttypes.ContentType{}).Schema()) {
		if err := registry.Register(schema); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	store := orm.New(backend, registry)
	runner := migrations.Executor{Backend: backend, Editor: backend.SchemaEditor(), Migrations: append(append(contenttypes.Migrations(), auth.Migrations()...), auth.PasswordResetMigrations()...)}
	if err := runner.Apply(ctx, ""); err != nil {
		t.Fatal(err)
	}
	allowReset := true
	accountConfig := auth.AccountsConfig{Store: store, Authorize: func(ctx context.Context, change auth.AccountChange) error {
		if auth.FromContext(ctx).ID == "fixture-operator" || allowReset && change.Action == "reset_password" {
			return nil
		}
		return auth.ErrPermissionDenied
	}}
	accounts, err := auth.NewAccounts(accountConfig)
	if err != nil {
		t.Fatal(err)
	}
	operator := auth.WithPrincipal(ctx, auth.Principal{ID: "fixture-operator", Authenticated: true, Active: true})
	user, err := accounts.CreateUser(operator, "reset-identity", "fixture original password", auth.CreateUserOptions{})
	if err != nil {
		t.Fatal(err)
	}
	box := &resetMailbox{}
	var events []string
	config := auth.PasswordResetConfig{Accounts: accounts, Mail: box, From: "support@example.test", ResetURL: "https://example.test/reset", RequestTimeout: 100 * time.Millisecond, VerifiedRecipient: func(_ context.Context, p auth.Principal) (string, error) {
		if p.ID != user.ID {
			return "", auth.ErrPermissionDenied
		}
		return "verified@example.test", nil
	}, OnEvent: func(_ context.Context, code string) { events = append(events, code) }}
	service, err := auth.NewPasswordReset(config)
	if err != nil {
		t.Fatal(err)
	}
	box.onSend = func(message mail.Message) {
		if db.InTransaction(ctx, backend.Alias()) {
			t.Fatal("mail sent in transaction")
		}
		// A second connection can see the committed token before transmission.
		count, err := orm.For(store, func() *auth.PasswordResetRecord { return &auth.PasswordResetRecord{} }).Filter(orm.Q("user_id", user.ID)).Count(ctx)
		if err != nil || count == 0 {
			t.Fatal("message preceded token commit", err)
		}
	}
	for _, identifier := range []string{"missing-identity", strings.Repeat("x", 513), ""} {
		started := time.Now()
		if err := service.Request(ctx, identifier); err != nil {
			t.Fatal(err)
		}
		if time.Since(started) < 90*time.Millisecond || len(box.messages) != 0 {
			t.Fatal("ineligible request timing/delivery")
		}
	}
	issue := func() string {
		t.Helper()
		before := len(box.messages)
		if err := service.Request(ctx, user.Identifier); err != nil {
			t.Fatal(err)
		}
		if len(box.messages) != before+1 {
			t.Fatal("eligible reset not delivered", events)
		}
		return box.bearer(t)
	}
	first := issue()
	row, err := orm.For(store, func() *auth.PasswordResetRecord { return &auth.PasswordResetRecord{} }).Filter(orm.Q("id", first[:36])).Get(ctx)
	if err != nil || row.AuthVersion != 1 || row.UsedAt != nil || len(row.SecretDigest) != 64 || strings.Contains(row.SecretDigest, first[37:]) || row.ExpiresAt.Sub(row.CreatedAt) != time.Hour {
		t.Fatal("token record invariant", err)
	}
	for _, test := range []struct {
		bearer, password string
		want             error
	}{
		{first[:37] + strings.Repeat("A", 43), "fixture changed password", auth.ErrResetToken},
		{first, "short", auth.ErrPasswordValidation},
	} {
		result, err := service.Confirm(ctx, test.bearer, test.password)
		if !errors.Is(err, test.want) || result.State != auth.PasswordUnchanged || result.Principal.ID != "" {
			t.Fatal("rejected reset outcome", result.State, err)
		}
	}
	allowReset = false
	if result, err := service.Confirm(ctx, first, "fixture changed password"); !errors.Is(err, auth.ErrPermissionDenied) || result.State != auth.PasswordUnchanged {
		t.Fatal(result.State, err)
	}
	allowReset = true
	if err := db.Atomic(ctx, backend, db.AtomicOptions{}, func(ctx context.Context) error {
		result, err := service.Confirm(ctx, first, "fixture changed password")
		if err == nil || result.State != auth.PasswordUnchanged {
			t.Fatal("ambient confirmation accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Two simultaneous consumers of the same real row have exactly one winner.
	start := make(chan struct{})
	out := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			result, err := service.Confirm(ctx, first, "  fixture changed password  ")
			if result.Principal.ID != "" {
				out <- errors.New("reset promoted login")
				return
			}
			out <- err
		}()
	}
	close(start)
	wins := 0
	for range 2 {
		err := <-out
		if err == nil {
			wins++
		} else if !errors.Is(err, auth.ErrResetToken) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("single-use winner count", wins)
	}
	row, err = orm.For(store, func() *auth.PasswordResetRecord { return &auth.PasswordResetRecord{} }).Filter(orm.Q("id", first[:36])).Get(ctx)
	principal, loadErr := accounts.LoadPrincipal(ctx, user.ID)
	if err != nil || loadErr != nil || row.UsedAt == nil || principal.AuthVersion != 2 {
		t.Fatal("consume and credential not atomic", err, loadErr)
	}
	authenticator, err := auth.NewAuthenticator(accounts, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authenticator.Authenticate(ctx, user.Identifier, "fixture original password"); !errors.Is(err, auth.ErrCredentials) {
		t.Fatal("old password remained valid", err)
	}
	if _, err := authenticator.Authenticate(ctx, user.Identifier, "  fixture changed password  "); err != nil {
		t.Fatal("new password failed", err)
	}
	// An unrelated credential transition invalidates all outstanding tokens.
	second := issue()
	if err := accounts.ChangePassword(operator, user.ID, "fixture privileged replacement"); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Confirm(ctx, second, "fixture retry password"); !errors.Is(err, auth.ErrResetToken) || result.State != auth.PasswordUnchanged {
		t.Fatal(result.State, err)
	}
	expired := issue()
	if _, err := backend.Exec(ctx, "UPDATE gogo_password_resets SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", expired[:36]); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Confirm(ctx, expired, "fixture retry password"); !errors.Is(err, auth.ErrResetToken) {
		t.Fatal("expired token accepted", err)
	}
	// Report an actually committed but uncertain result without login promotion.
	uncertain := issue()
	uncertainStore := orm.New(unknownAccountCommitBackend{backend}, registry)
	uncertainConfig := accountConfig
	uncertainConfig.Store = uncertainStore
	uncertainAccounts, err := auth.NewAccounts(uncertainConfig)
	if err != nil {
		t.Fatal(err)
	}
	uncertainResetConfig := config
	uncertainResetConfig.Accounts = uncertainAccounts
	uncertainService, err := auth.NewPasswordReset(uncertainResetConfig)
	if err != nil {
		t.Fatal(err)
	}
	result, err := uncertainService.Confirm(ctx, uncertain, "fixture uncertain replacement")
	if !db.IsCode(err, db.UnknownCommit) || result.State != auth.PasswordChangeUnknown || result.Principal.ID != "" {
		t.Fatal("uncertain outcome", result.State, err)
	}
	if _, err := service.Confirm(ctx, uncertain, "fixture retry password"); !errors.Is(err, auth.ErrResetToken) {
		t.Fatal("uncertain committed token replayed", err)
	}
	before := len(box.messages)
	if err := uncertainService.Request(ctx, user.Identifier); err != nil || len(box.messages) != before {
		t.Fatal("unknown token commit sent email", err)
	}
	// Extension callbacks cannot alter the locked proof or the approved final
	// credential transition, even when their nested writes use savepoints.
	for _, phase := range []string{"policy", "validator", "user_before", "token_before", "token_after", "token_pointer"} {
		t.Run(phase, func(t *testing.T) {
			bearer := issue()
			p, err := accounts.LoadPrincipal(ctx, user.ID)
			if err != nil {
				t.Fatal(err)
			}
			mutating := accountConfig
			entered := false
			change := func(ctx context.Context) error {
				if entered {
					return nil
				}
				entered = true
				return accounts.ChangePassword(auth.WithPrincipal(ctx, auth.FromContext(operator)), user.ID, "fixture nested reset replacement")
			}
			if phase == "policy" {
				mutating.Authorize = func(ctx context.Context, c auth.AccountChange) error {
					if err := accountConfig.Authorize(ctx, c); err != nil {
						return err
					}
					if c.Action == "reset_password" {
						return change(ctx)
					}
					return nil
				}
			}
			if phase == "validator" {
				mutating.PasswordValidators = []auth.PasswordValidator{func(ctx context.Context, _ string, _ auth.Principal) error { return change(ctx) }}
			}
			mutatingAccounts, err := auth.NewAccounts(mutating)
			if err != nil {
				t.Fatal(err)
			}
			mutatingResetConfig := config
			mutatingResetConfig.Accounts = mutatingAccounts
			mutatingService, err := auth.NewPasswordReset(mutatingResetConfig)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "user_before" || phase == "token_before" || phase == "token_pointer" {
				store.BeforeSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
					_, isUser := event.Model.(*auth.User)
					token, isToken := event.Model.(*auth.PasswordResetRecord)
					if phase == "token_pointer" && isToken && token.UsedAt != nil {
						value := token.UsedAt.Add(time.Hour)
						*token.UsedAt = value
						return nil
					}
					if phase == "user_before" && isUser || phase == "token_before" && isToken {
						return change(ctx)
					}
					return nil
				}}
			}
			if phase == "token_after" {
				store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
					if _, ok := event.Model.(*auth.PasswordResetRecord); ok {
						return change(ctx)
					}
					return nil
				}}
			}
			result, err := mutatingService.Confirm(ctx, bearer, "fixture outer reset replacement")
			store.BeforeSave = nil
			store.AfterSave = nil
			if err == nil || result.State != auth.PasswordUnchanged || result.Principal.ID != "" {
				t.Fatal("callback changed approved outcome", phase, result.State, err)
			}
			current, err := accounts.LoadPrincipal(ctx, user.ID)
			if err != nil || current.AuthVersion != p.AuthVersion {
				t.Fatal("callback escaped rollback", phase, err)
			}
			token, err := orm.For(store, func() *auth.PasswordResetRecord { return &auth.PasswordResetRecord{} }).Filter(orm.Q("id", bearer[:36])).Get(ctx)
			if err != nil || token.UsedAt != nil {
				t.Fatal("rejected callback consumed token", phase, err)
			}
		})
	}
	// After-commit effects cannot turn a durable replacement into 'unchanged'.
	afterCommit := issue()
	store.AfterSave = []orm.SaveReceiver{func(ctx context.Context, event orm.SaveEvent) error {
		if _, ok := event.Model.(*auth.PasswordResetRecord); !ok {
			return nil
		}
		return db.OnCommit(ctx, backend.Alias(), func(context.Context) error { return errors.New("fixture after-commit effect failed") }, false)
	}}
	result, err = service.Confirm(ctx, afterCommit, "fixture committed reset replacement")
	store.AfterSave = nil
	var committed *db.CommittedCallbackError
	if !errors.As(err, &committed) || result.State != auth.PasswordChanged || result.Principal.ID != "" {
		t.Fatal("committed failure misreported", result.State, err)
	}
	if _, err := service.Confirm(ctx, afterCommit, "fixture retry password"); !errors.Is(err, auth.ErrResetToken) {
		t.Fatal("committed failure token replayed", err)
	}
	before = len(box.messages)
	if err := accounts.SetUnusablePassword(operator, user.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Request(ctx, user.Identifier); err != nil || len(box.messages) != before {
		t.Fatal("unusable credential reset sent", err)
	}
	for _, event := range events {
		if strings.Contains(event, user.Identifier) || strings.Contains(event, "@") || strings.Contains(event, first) {
			t.Fatal("safe status disclosed sensitive data")
		}
	}
	// Provider/resolver failures, including panics, retain the generic request
	// acknowledgement. Their private text is never emitted through safe events.
	if err := accounts.ChangePassword(operator, user.ID, "fixture restored reset password"); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"resolver-error", "resolver-panic", "mail-error", "mail-panic", "inactive"} {
		t.Run(phase, func(t *testing.T) {
			faultConfig := config
			faultBox := &resetMailbox{}
			faultConfig.Mail = faultBox
			faultConfig.OnEvent = func(_ context.Context, code string) {
				if strings.Contains(code, "private") {
					t.Fatal("provider error leaked")
				}
			}
			switch phase {
			case "resolver-error":
				faultConfig.VerifiedRecipient = func(context.Context, auth.Principal) (string, error) { return "", errors.New("private recipient data") }
			case "resolver-panic":
				faultConfig.VerifiedRecipient = func(context.Context, auth.Principal) (string, error) { panic("private recipient data") }
			case "mail-error":
				faultBox.err = errors.New("private mail data")
			case "mail-panic":
				faultBox.onSend = func(mail.Message) { panic("private mail data") }
			case "inactive":
				if err := accounts.SetAccountFlags(operator, user.ID, false, false, false); err != nil {
					t.Fatal(err)
				}
			}
			faultService, err := auth.NewPasswordReset(faultConfig)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if err := faultService.Request(ctx, user.Identifier); err != nil {
				t.Fatal("request existence signal", err)
			}
			if time.Since(started) < 90*time.Millisecond {
				t.Fatal("fault bypassed timing floor")
			}
			if strings.HasPrefix(phase, "resolver-") || phase == "inactive" {
				if len(faultBox.messages) != 0 {
					t.Fatal("ineligible request delivered")
				}
			}
		})
	}
	for _, link := range []string{"http://example.test/reset", "//example.test/reset", "https://user:password@example.test/reset", "https://example.test/reset?token=x", "https://example.test/reset#x"} {
		invalid := config
		invalid.ResetURL = link
		if _, err := auth.NewPasswordReset(invalid); err == nil {
			t.Fatal("unsafe reset URL accepted")
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := service.Request(canceled, user.Identifier); !errors.Is(err, context.Canceled) {
		t.Fatal("request ignored cancellation", err)
	}
}
