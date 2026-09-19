package admin

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/templates"
)

func TestAccountProjectionLabelsPreserveIdentityAndCredentialRedaction(t *testing.T) {
	credential := "synthetic private credential hash"
	for _, model := range []models.Model{
		&auth.User{ID: "11111111-1111-4111-8111-111111111111", Identifier: `<script>account</script>`, PasswordHash: &credential, AuthVersion: 3},
		&auth.Group{ID: "22222222-2222-4222-8222-222222222222", Name: `<script>group</script>`},
	} {
		record, err := models.Bind(model)
		if err != nil {
			t.Fatal(err)
		}
		record.State().Persisted, record.State().Database = true, "fixture"
		original, err := objectFromRecord(record)
		if err != nil {
			t.Fatal(err)
		}
		projected, err := redactAccount(original)
		if err != nil || projected.ID != original.ID || projected.Label == original.Label || !projected.Record.State().Persisted || projected.Record.State().Database != "fixture" {
			t.Fatal("account label lost scoped identity/state", err)
		}
		canonical, err := objectFromRecord(projected.Record)
		if err != nil || projected.Version != canonical.Version || projected.ID != canonical.ID {
			t.Fatal("presentation label changed record token identity", err)
		}
		if _, user := model.(*auth.User); user {
			value, err := projected.Record.Get("password_hash")
			if err != nil || value != nil || projected.Record == record || projected.Label != `<script>account</script>` {
				t.Fatal("labeling lost hashless account projection", err)
			}
		} else if projected.Label != `<script>group</script>` || projected.Version != original.Version {
			t.Fatal("group label changed version or lost name")
		}
		site, _ := newTestSite(t)
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "http://example.test/admin/", nil)
		site.render(w, r, principal(), "form.html", templates.Context{"title": projected.Label}, 200)
		if w.Code != 200 || strings.Contains(w.Body.String(), projected.Label) || !strings.Contains(w.Body.String(), "&lt;script&gt;") {
			t.Fatal("account label was not escaped", w.Code)
		}
	}
}

func TestActorLabelIsEscapedBoundedAndFailsClosed(t *testing.T) {
	for _, mode := range []string{"default", "success", "provider", "panic", "cancel", "empty", "oversize", "invalid_utf8"} {
		t.Run(mode, func(t *testing.T) {
			site, _ := newTestSite(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			if mode != "default" {
				site.config.ActorLabel = func(ctx context.Context, p auth.Principal) (string, error) {
					calls++
					if auth.FromContext(ctx).ID != p.ID || p.ID != principal().ID {
						t.Fatal("actor display callback received an unbound identity")
					}
					switch mode {
					case "provider":
						return "partial private label", errors.New("synthetic private provider detail")
					case "panic":
						panic("synthetic private provider detail")
					case "cancel":
						cancel()
						return "partial private label", nil
					case "empty":
						return "", nil
					case "oversize":
						return strings.Repeat("a", 1025), nil
					case "invalid_utf8":
						return "\xff", nil
					}
					return `<script>display</script>`, nil
				}
			}
			w := httptest.NewRecorder()
			r := httptest.NewRequest("GET", "http://example.test/admin/", nil).WithContext(ctx)
			site.render(w, r, principal(), "index.html", templates.Context{"title": "Administration"}, 200)
			if mode == "default" {
				if w.Code != 200 || !strings.Contains(w.Body.String(), principal().ID) {
					t.Fatal("default actor identity changed")
				}
			} else if mode == "success" {
				if w.Code != 200 || strings.Contains(w.Body.String(), "<script>display</script>") || !strings.Contains(w.Body.String(), "&lt;script&gt;display&lt;/script&gt;") {
					t.Fatal("actor label not escaped", w.Code)
				}
			} else if w.Code != 503 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "<!doctype") {
				t.Fatal("actor provider failure exposed partial page", w.Code)
			}
			if mode != "default" && calls != 1 {
				t.Fatal("actor callback repeated")
			}
		})
	}
}

func TestCredentialInstructionUsesResponsiveFormInset(t *testing.T) {
	site, _ := newTestSite(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "http://example.test/admin/", nil)
	site.render(w, r, principal(), "user_credentials.html", templates.Context{"title": "Create account", "instruction": "Synthetic safety instruction"}, 200)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `<p>Synthetic safety instruction</p>`) {
		t.Fatal("credential instruction lost the shared form inset", w.Code)
	}
}

func TestActorLabelReceivesAnIndependentPermissionSnapshot(t *testing.T) {
	site, _ := newTestSite(t)
	p := principal()
	p.Permissions = []string{"shop.view_product"}
	p, err := auth.ConstrainPrincipal(p, []string{"shop.view_product"})
	if err != nil {
		t.Fatal(err)
	}
	site.config.ActorLabel = func(ctx context.Context, shown auth.Principal) (string, error) {
		shown.Permissions[0] = "shop.delete_product"
		if auth.FromContext(ctx).Permissions[0] != "shop.view_product" {
			t.Fatal("display callback argument aliases its context identity")
		}
		scopes, restricted := shown.TokenScopes()
		if !restricted || len(scopes) != 1 || scopes[0] != "shop.view_product" {
			t.Fatal("display callback lost token ceiling")
		}
		return "Reviewer", nil
	}
	if _, err := site.actorLabel(context.Background(), p); err != nil || p.Permissions[0] != "shop.view_product" {
		t.Fatal("display callback altered the caller identity", err)
	}
}
