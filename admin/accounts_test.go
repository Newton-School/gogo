package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Newton-School/gogo/core/auth"
	"github.com/Newton-School/gogo/core/db"
	"github.com/Newton-School/gogo/core/models"
	"github.com/Newton-School/gogo/core/orm"
)

type accountGraphBackend struct{ db.Backend }

func (accountGraphBackend) Alias() string { return "account-graph-test" }
func (accountGraphBackend) BeginTx(context.Context, db.TxOptions) (db.Transaction, error) {
	return accountGraphTransaction{}, nil
}

type accountGraphTransaction struct{ db.Transaction }

func (accountGraphTransaction) Commit() error   { return nil }
func (accountGraphTransaction) Rollback() error { return nil }

func TestDirectAccountStoreDeleteCannotBypassProtectedGraph(t *testing.T) {
	for _, kind := range []string{"object", "update", "join"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := models.Bind(&testRecord{ID: 1})
			user, _ := models.NewRecord((&auth.User{}).Schema())
			graph := Deletion{Objects: []Object{{Record: root}}}
			if kind == "object" {
				graph.Objects = append(graph.Objects, Object{Record: user})
			} else if kind == "update" {
				graph.Updates = []RelatedUpdate{{Object: Object{Record: user}, Field: "active", Value: false}}
			} else {
				graph.JoinRemovals = []JoinRemoval{{Endpoint: Object{Record: user}}}
			}
			mutated := false
			backend := accountGraphBackend{}
			owner := &ORMStore{config: ORMConfig{Store: orm.New(backend, nil), ValidateWrite: func(context.Context, auth.Principal, models.Record) error { return nil }, DeleteGraph: func(ctx context.Context, _ auth.Principal, _ models.Record, authorize func(context.Context, Deletion) error) error {
				if err := authorize(ctx, graph); err != nil {
					return err
				}
				mutated = true
				return nil
			}}}
			store := &accountScoped{ormScoped: &ormScoped{owner: owner, schema: root.Schema()}}
			err := db.Atomic(context.Background(), backend, db.AtomicOptions{}, func(ctx context.Context) error { return store.Delete(ctx, Object{Record: root}) })
			if !errors.Is(err, auth.ErrPermissionDenied) || mutated {
				t.Fatal("direct delete bypassed account graph protection", err)
			}
		})
	}
}

func TestModelAdminAuthorityOnlyNarrowsGlobalPolicy(t *testing.T) {
	site, _ := newTestSite(t)
	ctx := context.Background()
	calls := 0
	options := ModelAdmin{Schema: (&testRecord{}).Schema(), Authorize: func(context.Context, auth.Principal, string, Object) error {
		calls++
		return nil
	}}
	site.config.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return auth.ErrPermissionDenied })
	if err := site.allowed(ctx, principal(), "change", options, Object{}); !errors.Is(err, auth.ErrPermissionDenied) || calls != 0 {
		t.Fatal("model hook widened site authority", err)
	}
	site.config.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return nil })
	options.Authorize = func(context.Context, auth.Principal, string, Object) error { return auth.ErrPermissionDenied }
	if err := site.allowed(ctx, principal(), "change", options, Object{}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("model restriction not enforced", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	options.Authorize = func(context.Context, auth.Principal, string, Object) error { cancel(); return nil }
	if err := site.allowed(ctx, principal(), "change", options, Object{}); !errors.Is(err, context.Canceled) {
		t.Fatal("model callback cancellation ignored", err)
	}
}

func TestAccountStoreRejectsDifferentORMAndCredentialNamespaces(t *testing.T) {
	if _, err := NewAccountStore(AccountStoreConfig{ORM: ORMConfig{Store: &orm.Store{}}, Accounts: auth.AccountsConfig{Store: &orm.Store{}}}); err == nil {
		t.Fatal("different account/audit stores accepted")
	}
	store := &AccountStore{}
	for _, schema := range []models.Schema{(&auth.PasswordResetRecord{}).Schema(), {AppLabel: "gogo_authtokens", Name: "APIToken"}, {AppLabel: "gogo_auth", Name: "FutureCredential"}} {
		if _, err := store.Scope(context.Background(), principal(), "admin", schema); !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatal("credential record accepted as generic Admin surface", err)
		}
		record, err := models.NewRecord(models.Schema{AppLabel: schema.AppLabel, Name: schema.Name, Fields: []models.Field{models.BigAutoField("id")}})
		if err != nil {
			t.Fatal(err)
		}
		if err := protectAccountDeletion(Deletion{Objects: []Object{{Record: record}}}); !errors.Is(err, auth.ErrPermissionDenied) {
			t.Fatal("credential deletion graph accepted", err)
		}
	}
}

func TestAdminTokenScopeCeilingSurvivesSuperuserAndCustomPolicy(t *testing.T) {
	prior, _ := newTestSite(t)
	config := prior.config
	config.Policy = auth.PolicyFunc(func(context.Context, auth.Principal, string, auth.Resource) error { return nil })
	site, err := NewSite(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := site.Register(ModelAdmin{Schema: (&testRecord{}).Schema(), Fields: []string{"Name"}}); err != nil {
		t.Fatal(err)
	}
	p := principal()
	p.Superuser = true
	p, err = auth.ConstrainPrincipal(p, []string{"shop.view_product"})
	if err != nil {
		t.Fatal(err)
	}
	get := perform(site, "GET", "/admin/shop/product/1/change/", p, nil, nil)
	if get.Code != 200 || strings.Contains(get.Body.String(), `name="Name"`) {
		t.Fatal("view-only token exposed change form", get.Code)
	}
	if err := site.config.Policy.Authorize(context.Background(), p, "change", auth.Resource{App: "shop", Model: "Product"}); !errors.Is(err, auth.ErrPermissionDenied) {
		t.Fatal("direct inline/action policy bypassed token ceiling", err)
	}
	add := perform(site, "GET", "/admin/shop/product/add/", p, nil, nil)
	if add.Code != 403 {
		t.Fatal("superuser token ceiling widened by custom policy", add.Code)
	}
}
